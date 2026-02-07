package PublishToSlack

import (
	"fmt"
	"strings"

	"slack-tag-summariser/Models"

	"github.com/slack-go/slack"
)

type GenAiResponse = Models.GenAiResponse

func formatGenAiResponsesVertical(responses []GenAiResponse) string {
	var b strings.Builder

	for i, r := range responses {
		// 1. Header with Emoji & Link
		b.WriteString(fmt.Sprintf("🔗 *Mention Link:* <%s|Click Here> |\n", r.MentionPermalink))

		// 2. Priority-based Emoji logic
		priorityEmoji := "⚪" // Default
		switch strings.ToUpper(r.Priority) {
		case "P0", "P1":
			priorityEmoji = "🚨"
		case "P2":
			priorityEmoji = "⚠️"
		case "P3":
			priorityEmoji = "🔵"
		}

		// Actionable Emoji
		actionEmoji := "✅"
		if strings.ToLower(r.Actionable) == "no" {
			actionEmoji = "➖"
		}

		// 3. Status Row
		b.WriteString(fmt.Sprintf("%s *Actionable:* %s.     %s *Priority:* `%s`\n", actionEmoji, r.Actionable, priorityEmoji, r.Priority))

		// 4. Summary Section (with a nice header emoji)
		b.WriteString("\n📝 *Summary*\n")
		for j, s := range r.Summary {
			b.WriteString(fmt.Sprintf("  %d. %s\n", j+1, s))
		}

		// 5. Action Required Section
		if len(r.ActionRequired) > 0 {
			b.WriteString("\n🛠️ *Action Required*\n")
			for _, a := range r.ActionRequired {
				b.WriteString(fmt.Sprintf("  • %s\n", a)) // Using bullets for actions for variety
			}
		}

		// 6. Styled Divider
		if i < len(responses)-1 {
			b.WriteString("\n━━━━━━━━━━━━━━━━━━━━━━━━━━━━\n\n")
		}
	}

	return b.String()
}

func SendSlackDm(slackClient *slack.Client, userId string, processUserResult []GenAiResponse) (bool, error) {
	msg := formatGenAiResponsesVertical(processUserResult)

	_, _, sendSlackDmError := slackClient.PostMessage(
		userId,
		slack.MsgOptionText(msg, false),
		// This is the key part to disable previews
		slack.MsgOptionPostMessageParameters(slack.PostMessageParameters{
			UnfurlLinks: false,
			UnfurlMedia: false,
		}),
	)

	if sendSlackDmError != nil {
		return false, sendSlackDmError
	}

	return true, nil
}
