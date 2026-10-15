package main

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"
)

const testMailbox = "urn:li:fsd_profile:ACoSelf"
const testConversationURN = "urn:li:msg_conversation:(urn:li:fsd_profile:ACoSelf,2-example)"

// These fixtures use the observed HAR envelopes with synthetic identities.
const testConversationJSON = `{"data":{"messengerConversationsByRecipients":{"elements":[{
  "entityUrn":"urn:li:msg_conversation:(urn:li:fsd_profile:ACoSelf,2-example)",
  "conversationUrl":"https://www.linkedin.com/messaging/thread/2-example/",
  "groupChat":false,"conversationParticipants":[
    {"hostIdentityUrn":"urn:li:fsd_profile:ACoSelf"},
    {"hostIdentityUrn":"urn:li:fsd_profile:ACoExamplePerson"}
  ]
}]}}}`

func messageTestClient(t *testing.T, conversation, messages string, post roundTripFunc, compose ...string) *linkedinClient {
	t.Helper()
	client := &linkedinClient{session: session{LIAT: "test-token", JSessionID: "ajax:test"}}
	client.http = &http.Client{Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
		// Production spacing belongs to the shared HTTP boundary; tests never
		// access the network and need not spend two seconds per mock response.
		defer func() { client.lastRequestAt = time.Time{} }()
		if r.Header.Get("Csrf-Token") != "ajax:test" {
			t.Fatal("message request omitted CSRF token")
		}
		if r.Method == http.MethodPost {
			if post == nil {
				t.Fatalf("unexpected live mutation in preview/read: %s", r.URL)
			}
			return post(r)
		}
		switch r.URL.Path {
		case "/voyager/api/voyagerMessagingDashComposeOptions/urn:li:fsd_composeOption:(ACoExamplePerson,NON_SELF_PROFILE_VIEW,EMPTY_CONTEXT_ENTITY_URN)":
			body := `{"data":{"entityUrn":"urn:li:fsd_composeOption:(ACoExamplePerson,NON_SELF_PROFILE_VIEW,EMPTY_CONTEXT_ENTITY_URN)","composeOptionType":"CONNECTION_MESSAGE","composeNavigationContext":{"recipientUrns":["urn:li:fsd_profile:ACoExamplePerson"],"paidInMail":false}}}`
			if len(compose) > 0 {
				body = strings.ReplaceAll(body, "CONNECTION_MESSAGE", "PREMIUM_INMAIL")
			}
			return jsonResponse([]byte(body)), nil
		case "/voyager/api/graphql":
			if len(compose) == 0 || r.URL.Query().Get("queryId") != "voyagerMessagingDashComposeViewContexts.e3682e895986d41a9c3276d8072c984a" {
				t.Fatalf("unexpected compose context query: %s", r.URL)
			}
			footer := "Free message | Why?"
			if compose[0] == "paid" {
				footer = "Use 1 InMail credit"
			}
			return jsonResponse([]byte(`{"data":{"data":{"messagingDashComposeViewContextsByRecipients":{"elements":[{"showBlockedFooter":false,"footerText":{"text":"` + footer + `"}}]}}}}`)), nil
		case "/voyager/api/me":
			return jsonResponse([]byte(`{"data":{"*miniProfile":"urn:li:fs_miniProfile:ACoSelf"}}`)), nil
		case "/voyager/api/identity/dash/profiles":
			return jsonResponse(fixture(t, "profile")), nil
		case "/voyager/api/voyagerMessagingGraphQL/graphql":
			variables := r.URL.Query().Get("variables")
			switch r.URL.Query().Get("queryId") {
			case conversationsByRecipientsQueryID:
				if variables != "(mailboxUrn:urn:li:fsd_profile:ACoSelf,recipients:List(urn:li:fsd_profile:ACoExamplePerson))" {
					t.Fatalf("wrong conversation lookup: %q", variables)
				}
				return jsonResponse([]byte(conversation)), nil
			case messagesBySyncTokenQueryID:
				if variables != "(conversationUrn:"+testConversationURN+")" {
					t.Fatalf("wrong message lookup: %q", variables)
				}
				return jsonResponse([]byte(messages)), nil
			}
		}
		t.Fatalf("unexpected request: %s %s", r.Method, r.URL)
		return nil, errors.New("unexpected request")
	})}
	return client
}

func TestMessagesReadVerifiesDirectParticipants(t *testing.T) {
	for _, tc := range []struct {
		name         string
		conversation string
		messages     string
		wantError    bool
	}{
		{"direct", testConversationJSON, `{"data":{"messengerMessagesBySyncToken":{"elements":[{"entityUrn":"message-1","body":{"text":"Привет!\nhttps://example.com/"},"sender":{"hostIdentityUrn":"urn:li:fsd_profile:ACoSelf"}}]}}}`, false},
		{"wrong participant", strings.ReplaceAll(testConversationJSON, "ACoExamplePerson", "ACoSomeoneElse"), "", true},
		{"group", strings.ReplaceAll(testConversationJSON, `"groupChat":false`, `"groupChat":true`), "", true},
		{"missing conversations", `{"data":{}}`, "", true},
		{"query rejected", `{"data":{"errors":[{"message":"restricted"}]}}`, "", true},
		{"missing messages", testConversationJSON, `{"data":{}}`, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			client := messageTestClient(t, tc.conversation, tc.messages, nil)
			result, err := client.messages(context.Background(), profileRef{Slug: "example-person"})
			if tc.wantError {
				if err == nil || len(result.Messages) != 0 {
					t.Fatalf("unverified response succeeded: %+v, %v", result, err)
				}
				return
			}
			if err != nil || result.Recipient.Name != "Example Person" || result.MailboxURN != testMailbox || result.ConversationURN != testConversationURN || len(result.Messages) != 1 || result.Messages[0].Body.Text != "Привет!\nhttps://example.com/" {
				t.Fatalf("wrong direct thread: %+v, %v", result, err)
			}
		})
	}
}

func TestMessagePreviewAndDuplicateDoNotSend(t *testing.T) {
	const text = "Привет!\nhttps://example.com/"
	for _, tc := range []struct {
		name     string
		messages string
		execute  bool
		present  bool
	}{
		{"preview", `{"data":{"messengerMessagesBySyncToken":{"elements":[]}}}`, false, false},
		{"duplicate", `{"data":{"messengerMessagesBySyncToken":{"elements":[{"entityUrn":"message-1","body":{"text":"Привет!\nhttps://example.com/"},"sender":{"hostIdentityUrn":"urn:li:fsd_profile:ACoSelf"}}]}}}`, true, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			client := messageTestClient(t, testConversationJSON, tc.messages, nil)
			result, err := client.sendMessage(context.Background(), profileRef{Slug: "example-person"}, text, tc.execute)
			if err != nil || result.Sent || result.AlreadyPresent != tc.present || result.Text != text || result.Recipient.Name != "Example Person" {
				t.Fatalf("preview/duplicate result: %+v, %v", result, err)
			}
		})
	}
}

func TestMessageSendUsesObservedContractAndNeverRetries(t *testing.T) {
	const text = "Привет!\nhttps://example.com/?x=1&y=2\nПожалуйста?"
	for _, outcome := range []string{"receipt", "free inmail", "wrong token", "wrong text", "rate limit", "timeout"} {
		t.Run(outcome, func(t *testing.T) {
			posts := 0
			var compose []string
			if outcome == "free inmail" {
				compose = []string{"free"}
			}
			client := messageTestClient(t, `{"data":{"messengerConversationsByRecipients":{"elements":[]}}}`, "", func(r *http.Request) (*http.Response, error) {
				posts++
				if r.URL.Path != "/voyager/api/voyagerMessagingDashMessengerMessages" || r.URL.Query().Get("action") != "createMessage" || r.Header.Get("Content-Type") != "text/plain;charset=UTF-8" {
					t.Fatalf("wrong send endpoint or headers: %s", r.URL)
				}
				var request struct {
					Message struct {
						Body               messageBody       `json:"body"`
						OriginToken        string            `json:"originToken"`
						RenderContentUnion []json.RawMessage `json:"renderContentUnions"`
					} `json:"message"`
					MailboxURN  string   `json:"mailboxUrn"`
					Recipients  []string `json:"hostRecipientUrns"`
					TrackingID  string   `json:"trackingId"`
					HostContent map[string]struct {
						Union map[string]json.RawMessage `json:"messageCreateContentUnion"`
					} `json:"hostMessageCreateContent"`
				}
				if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
					t.Fatal(err)
				}
				if request.MailboxURN != testMailbox || len(request.Recipients) != 1 || request.Recipients[0] != "urn:li:fsd_profile:ACoExamplePerson" || request.Message.Body.Text != text || request.Message.Body.Attributes == nil || len(request.Message.Body.Attributes) != 0 || request.Message.RenderContentUnion == nil || len(request.Message.OriginToken) != 36 || len([]rune(request.TrackingID)) != 16 {
					t.Fatalf("send changed recipient, plain text, or wire contract: %+v", request)
				}
				_, inmail := request.HostContent["com.linkedin.voyager.dash.messaging.MessageCreateContent"].Union["premiumInMail"]
				if inmail != (outcome == "free inmail") {
					t.Fatalf("wrong InMail wire contract: %+v", request.HostContent)
				}
				if outcome == "timeout" {
					return nil, context.DeadlineExceeded
				}
				if outcome == "rate limit" {
					return &http.Response{StatusCode: 429, Header: http.Header{"Retry-After": {"60"}}, Body: io.NopCloser(strings.NewReader(""))}, nil
				}
				message := messengerMessage{URN: "urn:li:msg_message:(" + testMailbox + ",message-1)", ConversationURN: testConversationURN, SenderURN: "urn:li:msg_messagingParticipant:" + testMailbox, OriginToken: request.Message.OriginToken, Body: messageBody{Text: text}, DeliveredAt: 1234}
				if outcome == "wrong token" {
					message.OriginToken = "another-token"
				}
				if outcome == "wrong text" {
					message.Body.Text = "different message"
				}
				body, err := json.Marshal(struct {
					Value messengerMessage `json:"value"`
				}{message})
				if err != nil {
					t.Fatal(err)
				}
				return jsonResponse(body), nil
			}, compose...)
			result, err := client.sendMessage(context.Background(), profileRef{Slug: "example-person"}, text, true)
			if posts != 1 {
				t.Fatalf("send made %d POSTs", posts)
			}
			if outcome == "receipt" || outcome == "free inmail" {
				if err != nil || !result.Sent || result.Text != text || result.ConversationURL != "https://www.linkedin.com/messaging/thread/2-example/" {
					t.Fatalf("matching receipt failed: %+v, %v", result, err)
				}
			} else if err == nil || result.Sent || !strings.Contains(err.Error(), "read the conversation before retrying") {
				t.Fatalf("unconfirmed send reported success or omitted recovery guidance: %+v, %v", result, err)
			}
		})
	}
}

func TestPaidInMailDoesNotSend(t *testing.T) {
	client := messageTestClient(t, `{"data":{"messengerConversationsByRecipients":{"elements":[]}}}`, "", nil, "paid")
	result, err := client.sendMessage(context.Background(), profileRef{Slug: "example-person"}, "Approved message", true)
	if err == nil || !strings.Contains(err.Error(), "free InMail") || result.Sent {
		t.Fatalf("paid or unverified InMail accepted: %+v, %v", result, err)
	}
}
