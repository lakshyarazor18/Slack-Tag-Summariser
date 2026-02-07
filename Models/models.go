package Models

type UniqueMention struct {
	Timestamp string
	ChannelId string
}

type ConversationsResponse struct {
	// I want immutability
	// The struct is not big enough to make a pointer
	// hence using the slice directly for ConversationResponseEntry
	ConversationContext []ConversationResponseEntry
}

type ThreadMessage struct {
	Text      string
	Timestamp string
}
type ConversationResponseEntry struct {
	MentionPermalink string
	MentionText      string
	MentionChannelId string
	MentionTimestamp string
	Messages         []ThreadMessage
}

type GenAiResponse struct {
	MentionPermalink string
	Summary          []string `json:"summary"`
	Actionable       string   `json:"actionable"`
	ActionRequired   []string `json:"action_required"`
	Priority         string   `json:"priority"`
}

type User struct {
	UserID string
}
