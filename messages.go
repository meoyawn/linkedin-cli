package main

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"strings"
	"unicode/utf8"
)

// Observed in the messaging HAR. These endpoints return inline JSON, rather
// than the normalized entities used by company/profile queries.
const (
	conversationsByRecipientsQueryID = "messengerConversations.9c3ab648b616451570c715e4a184465e"
	messagesBySyncTokenQueryID       = "messengerMessages.5846eeb71c981f11e0134cb6626cc314"
	composeViewContextsQueryID       = "voyagerMessagingDashComposeViewContexts.e3682e895986d41a9c3276d8072c984a"
	connectionComposeOption          = "CONNECTION_MESSAGE"
	premiumInMailComposeOption       = "PREMIUM_INMAIL"
)

type messageBody struct {
	Attributes []json.RawMessage `json:"attributes"`
	Text       string            `json:"text"`
}

type messagingParticipant struct {
	ProfileURN string `json:"hostIdentityUrn"`
}

type messengerMessage struct {
	URN             string               `json:"entityUrn"`
	ConversationURN string               `json:"conversationUrn,omitempty"`
	OriginToken     string               `json:"originToken"`
	Body            messageBody          `json:"body"`
	DeliveredAt     int64                `json:"deliveredAt"`
	Sender          messagingParticipant `json:"sender"`
	SenderURN       string               `json:"senderUrn,omitempty"`
	RenderContent   []json.RawMessage    `json:"renderContent,omitempty"`
}

type messengerConversation struct {
	URN          string                 `json:"entityUrn"`
	URL          string                 `json:"conversationUrl"`
	Group        bool                   `json:"groupChat"`
	Participants []messagingParticipant `json:"conversationParticipants"`
}

type messagesResult struct {
	Recipient       person             `json:"recipient"`
	MailboxURN      string             `json:"mailbox_urn"`
	ConversationURN string             `json:"conversation_urn,omitempty"`
	ConversationURL string             `json:"conversation_url,omitempty"`
	Messages        []messengerMessage `json:"messages"`
}

type messageSendResult struct {
	Recipient       person `json:"recipient"`
	Text            string `json:"text"`
	Sent            bool   `json:"sent"`
	AlreadyPresent  bool   `json:"already_present"`
	MessageURN      string `json:"message_urn,omitempty"`
	ConversationURL string `json:"conversation_url,omitempty"`
	OriginToken     string `json:"origin_token,omitempty"`
	FreeInMail      bool   `json:"free_inmail,omitempty"`
}

func runMessages(ctx context.Context, args []string, store authStore, stdout, stderr io.Writer) error {
	if len(args) == 0 || isHelpFlag(args[0]) {
		return writeHelp(stdout, []string{"messages"})
	}
	command := args[0]
	if command != "read" && command != "send" {
		return fmt.Errorf("unknown messages command %q; expected read or send", command)
	}
	fs := flag.NewFlagSet("messages "+command, flag.ContinueOnError)
	fs.SetOutput(stderr)
	fs.Usage = func() { _ = writeHelp(stderr, []string{"messages", command}) }
	var textFile string
	var execute bool
	if command == "send" {
		fs.StringVar(&textFile, "text-file", "", "UTF-8 plain text file containing the exact approved message")
		fs.BoolVar(&execute, "send", false, "send the message after human approval; otherwise preview only")
	}
	positionals, err := parseFlags(fs, args[1:])
	if errors.Is(err, flag.ErrHelp) {
		return writeHelp(stdout, []string{"messages", command})
	}
	if err != nil {
		return err
	}
	if len(positionals) != 1 {
		return fmt.Errorf("messages %s requires one recipient profile; got %d arguments", command, len(positionals))
	}
	ref, err := parseProfile(positionals[0])
	if err != nil {
		return err
	}
	var text string
	if command == "send" {
		if textFile == "" {
			return fmt.Errorf("messages send requires --text-file with the exact approved plain text")
		}
		f, err := os.Open(textFile)
		if err != nil {
			return fmt.Errorf("open --text-file %q: %w", textFile, err)
		}
		body, readErr := io.ReadAll(io.LimitReader(f, 1<<20+1))
		f.Close()
		if readErr != nil {
			return fmt.Errorf("read --text-file %q: %w", textFile, readErr)
		}
		if len(body) > 1<<20 || !utf8.Valid(body) || strings.TrimSpace(string(body)) == "" {
			return fmt.Errorf("--text-file %q must contain nonempty UTF-8 text no larger than 1 MiB", textFile)
		}
		text = string(body)
	}
	client, err := newLinkedinClient(store)
	if err != nil {
		return err
	}
	if command == "read" {
		result, err := client.messages(ctx, ref)
		if err != nil {
			return err
		}
		return writeJSON(stdout, result)
	}
	result, err := client.sendMessage(ctx, ref, text, execute)
	if err != nil {
		return err
	}
	return writeJSON(stdout, result)
}

func (c *linkedinClient) mailbox(ctx context.Context) (string, error) {
	type miniProfile struct {
		URN string `json:"entityUrn"`
	}
	var response struct {
		Data struct {
			MiniProfileURN string      `json:"*miniProfile"`
			MiniProfile    miniProfile `json:"miniProfile"`
		} `json:"data"`
	}
	if err := c.getJSON(ctx, "/voyager/api/me", &response); err != nil {
		return "", fmt.Errorf("identify signed-in mailbox: %w", err)
	}
	urn := response.Data.MiniProfileURN
	if urn == "" {
		urn = response.Data.MiniProfile.URN
	}
	id, ok := strings.CutPrefix(urn, "urn:li:fs_miniProfile:")
	if !ok || !profileID.MatchString(id) {
		return "", fmt.Errorf("LinkedIn self response has changed (missing miniProfile identifier)")
	}
	return "urn:li:fsd_profile:" + id, nil
}

func (c *linkedinClient) messagingQuery(ctx context.Context, queryID, variables string, out any) error {
	resp, err := c.get(ctx, "/voyager/api/voyagerMessagingGraphQL/graphql?queryId="+queryID+"&variables="+variables,
		http.Header{"Accept": {"application/json"}})
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	var raw json.RawMessage
	if err := json.NewDecoder(io.LimitReader(resp.Body, 32<<20)).Decode(&raw); err != nil {
		return fmt.Errorf("LinkedIn messaging did not return valid JSON")
	}
	var envelope struct {
		Errors []json.RawMessage `json:"errors"`
		Data   struct {
			Errors []json.RawMessage `json:"errors"`
		} `json:"data"`
	}
	if err := json.Unmarshal(raw, &envelope); err != nil {
		return fmt.Errorf("decode LinkedIn messaging envelope: %w", err)
	}
	if len(envelope.Errors) > 0 || len(envelope.Data.Errors) > 0 {
		return fmt.Errorf("LinkedIn rejected messaging query %s; stopped without retrying", queryID)
	}
	if err := json.Unmarshal(raw, out); err != nil {
		return fmt.Errorf("decode LinkedIn messaging response: %w", err)
	}
	return nil
}

func (c *linkedinClient) messages(ctx context.Context, ref profileRef) (messagesResult, error) {
	mailbox, err := c.mailbox(ctx)
	if err != nil {
		return messagesResult{}, err
	}
	recipient, err := c.profile(ctx, ref)
	if err != nil {
		return messagesResult{}, err
	}
	result := messagesResult{Recipient: recipient.person, MailboxURN: mailbox, Messages: []messengerMessage{}}
	var response struct {
		Data struct {
			Conversations *struct {
				Elements []messengerConversation `json:"elements"`
			} `json:"messengerConversationsByRecipients"`
		} `json:"data"`
	}
	variables := "(mailboxUrn:" + url.QueryEscape(mailbox) + ",recipients:List(" + url.QueryEscape(recipient.ProfileURN) + "))"
	if err := c.messagingQuery(ctx, conversationsByRecipientsQueryID, variables, &response); err != nil {
		return messagesResult{}, err
	}
	if response.Data.Conversations == nil || response.Data.Conversations.Elements == nil {
		return messagesResult{}, fmt.Errorf("LinkedIn messaging response has changed (missing conversations)")
	}
	conversations := response.Data.Conversations.Elements
	if len(conversations) == 0 {
		return result, nil
	}
	if len(conversations) != 1 {
		return messagesResult{}, fmt.Errorf("LinkedIn returned %d conversations for one recipient; stopped to avoid selecting the wrong thread", len(conversations))
	}
	conversation := conversations[0]
	participants := map[string]bool{}
	for _, p := range conversation.Participants {
		participants[p.ProfileURN] = true
	}
	if conversation.Group || len(conversation.Participants) != 2 || len(participants) != 2 || !participants[mailbox] || !participants[recipient.ProfileURN] ||
		!strings.HasPrefix(conversation.URN, "urn:li:msg_conversation:("+mailbox+",") || !strings.HasSuffix(conversation.URN, ")") {
		return messagesResult{}, fmt.Errorf("LinkedIn conversation does not confirm the exact recipient and signed-in mailbox")
	}
	result.ConversationURN, result.ConversationURL = conversation.URN, conversation.URL
	var messages struct {
		Data struct {
			Messages *struct {
				Elements []messengerMessage `json:"elements"`
			} `json:"messengerMessagesBySyncToken"`
		} `json:"data"`
	}
	if err := c.messagingQuery(ctx, messagesBySyncTokenQueryID, "(conversationUrn:"+url.QueryEscape(conversation.URN)+")", &messages); err != nil {
		return messagesResult{}, err
	}
	if messages.Data.Messages == nil || messages.Data.Messages.Elements == nil {
		return messagesResult{}, fmt.Errorf("LinkedIn messaging response has changed (missing messages)")
	}
	result.Messages = messages.Data.Messages.Elements
	return result, nil
}

// Compose options and the free-message footer were observed in the signed-in
// browser. The premiumInMail payload comes from its loaded messaging client.
// A Message button alone does not establish permission to send normal messages.
func (c *linkedinClient) freeInMail(ctx context.Context, recipient person) (bool, error) {
	id, ok := strings.CutPrefix(recipient.ProfileURN, "urn:li:fsd_profile:")
	if !ok || !profileID.MatchString(id) {
		return false, fmt.Errorf("invalid messaging recipient profile URN %q", recipient.ProfileURN)
	}
	expectedURN := "urn:li:fsd_composeOption:(" + id + ",NON_SELF_PROFILE_VIEW,EMPTY_CONTEXT_ENTITY_URN)"
	var options struct {
		Data struct {
			URN     string `json:"entityUrn"`
			Type    string `json:"composeOptionType"`
			Context struct {
				Recipients []string `json:"recipientUrns"`
				Paid       *bool    `json:"paidInMail"`
			} `json:"composeNavigationContext"`
		} `json:"data"`
	}
	if err := c.getJSON(ctx, "/voyager/api/voyagerMessagingDashComposeOptions/"+url.PathEscape(expectedURN), &options); err != nil {
		return false, fmt.Errorf("read messaging compose options: %w", err)
	}
	if options.Data.URN != expectedURN || len(options.Data.Context.Recipients) != 1 || options.Data.Context.Recipients[0] != recipient.ProfileURN {
		return false, fmt.Errorf("compose options do not confirm the exact recipient %s", recipient.URL)
	}
	switch options.Data.Type {
	case connectionComposeOption:
		return false, nil
	case premiumInMailComposeOption:
		if options.Data.Context.Paid == nil || *options.Data.Context.Paid {
			return false, fmt.Errorf("only verified free InMail is supported; paid or unknown InMail stopped without sending")
		}
	default:
		return false, fmt.Errorf("unsupported compose option %q for %s; stopped without sending", options.Data.Type, recipient.URL)
	}
	var view struct {
		Data struct {
			Data struct {
				Contexts struct {
					Elements []struct {
						Blocked bool     `json:"showBlockedFooter"`
						Footer  textView `json:"footerText"`
					} `json:"elements"`
				} `json:"messagingDashComposeViewContextsByRecipients"`
			} `json:"data"`
		} `json:"data"`
	}
	variables := "(recipients:List(" + url.QueryEscape(recipient.ProfileURN) + "),type:" + premiumInMailComposeOption + ")"
	if err := c.getJSON(ctx, "/voyager/api/graphql?variables="+variables+"&queryId="+composeViewContextsQueryID, &view); err != nil {
		return false, fmt.Errorf("verify free InMail: %w", err)
	}
	contexts := view.Data.Data.Contexts.Elements
	if len(contexts) != 1 || contexts[0].Blocked || !strings.HasPrefix(contexts[0].Footer.Text, "Free message") {
		return false, fmt.Errorf("LinkedIn did not confirm free InMail for %s; stopped without sending or spending credits", recipient.URL)
	}
	return true, nil
}

func (c *linkedinClient) sendMessage(ctx context.Context, ref profileRef, text string, execute bool) (messageSendResult, error) {
	if !utf8.ValidString(text) || strings.TrimSpace(text) == "" || len(text) > 1<<20 {
		return messageSendResult{}, fmt.Errorf("message text must be nonempty UTF-8 no larger than 1 MiB")
	}
	thread, err := c.messages(ctx, ref)
	if err != nil {
		return messageSendResult{}, err
	}
	result := messageSendResult{Recipient: thread.Recipient, Text: text, ConversationURL: thread.ConversationURL}
	for _, m := range thread.Messages {
		if m.Body.Text == text && (m.Sender.ProfileURN == thread.MailboxURN || m.SenderURN == "urn:li:msg_messagingParticipant:"+thread.MailboxURN) {
			result.AlreadyPresent, result.MessageURN = true, m.URN
			return result, nil
		}
	}
	if thread.ConversationURN == "" {
		result.FreeInMail, err = c.freeInMail(ctx, thread.Recipient)
		if err != nil {
			return messageSendResult{}, err
		}
	}
	if !execute {
		return result, nil
	}
	// Keep a unique token in diagnostics after any ambiguous outcome. No retry is
	// performed: the conversation must be read before a human decides to resend.
	tokenBytes := make([]byte, 16)
	if _, err := rand.Read(tokenBytes); err != nil {
		return messageSendResult{}, fmt.Errorf("generate message origin token: %w", err)
	}
	tokenBytes[6] = tokenBytes[6]&0x0f | 0x40
	tokenBytes[8] = tokenBytes[8]&0x3f | 0x80
	token := fmt.Sprintf("%x-%x-%x-%x-%x", tokenBytes[:4], tokenBytes[4:6], tokenBytes[6:8], tokenBytes[8:10], tokenBytes[10:])
	tracking := make([]byte, 16)
	if _, err := rand.Read(tracking); err != nil {
		return messageSendResult{}, fmt.Errorf("generate messaging tracking ID: %w", err)
	}
	trackingRunes := make([]rune, len(tracking))
	for i, b := range tracking {
		trackingRunes[i] = rune(b)
	}
	payload := struct {
		Message struct {
			Body               messageBody       `json:"body"`
			OriginToken        string            `json:"originToken"`
			RenderContentUnion []json.RawMessage `json:"renderContentUnions"`
		} `json:"message"`
		MailboxURN       string         `json:"mailboxUrn"`
		TrackingID       string         `json:"trackingId"`
		Dedupe           bool           `json:"dedupeByClientGeneratedToken"`
		HostRecipientURN []string       `json:"hostRecipientUrns"`
		HostContent      map[string]any `json:"hostMessageCreateContent,omitempty"`
	}{MailboxURN: thread.MailboxURN, TrackingID: string(trackingRunes), HostRecipientURN: []string{thread.Recipient.ProfileURN}}
	if result.FreeInMail {
		payload.HostContent = map[string]any{
			"com.linkedin.voyager.dash.messaging.MessageCreateContent": map[string]any{
				"messageCreateContentUnion": map[string]any{"premiumInMail": struct{}{}},
			},
		}
	}
	payload.Message.Body = messageBody{Attributes: []json.RawMessage{}, Text: text}
	payload.Message.OriginToken = token
	payload.Message.RenderContentUnion = []json.RawMessage{}
	body, err := json.Marshal(payload)
	if err != nil {
		return messageSendResult{}, fmt.Errorf("encode message: %w", err)
	}
	resp, err := c.request(ctx, http.MethodPost, "/voyager/api/voyagerMessagingDashMessengerMessages?action=createMessage", bytes.NewReader(body),
		http.Header{"Accept": {"application/json"}, "Content-Type": {"text/plain;charset=UTF-8"}})
	if err != nil {
		return messageSendResult{}, fmt.Errorf("message send not confirmed (origin token %s); read the conversation before retrying: %w", token, err)
	}
	defer resp.Body.Close()
	var response struct {
		Value messengerMessage `json:"value"`
	}
	if err := json.NewDecoder(io.LimitReader(resp.Body, 1<<20)).Decode(&response); err != nil {
		return messageSendResult{}, fmt.Errorf("message send not confirmed (origin token %s); invalid receipt, read the conversation before retrying", token)
	}
	m := response.Value
	if !strings.HasPrefix(m.URN, "urn:li:msg_message:("+thread.MailboxURN+",") || m.OriginToken != token || m.Body.Text != text || m.DeliveredAt <= 0 ||
		m.SenderURN != "urn:li:msg_messagingParticipant:"+thread.MailboxURN || !strings.HasPrefix(m.ConversationURN, "urn:li:msg_conversation:("+thread.MailboxURN+",") {
		return messageSendResult{}, fmt.Errorf("message send not confirmed (origin token %s); unexpected receipt, read the conversation before retrying", token)
	}
	result.Sent, result.MessageURN, result.OriginToken = true, m.URN, token
	if id, ok := strings.CutPrefix(m.ConversationURN, "urn:li:msg_conversation:("+thread.MailboxURN+","); ok {
		result.ConversationURL = "https://www.linkedin.com/messaging/thread/" + url.PathEscape(strings.TrimSuffix(id, ")")) + "/"
	}
	return result, nil
}
