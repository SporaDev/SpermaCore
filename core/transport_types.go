package main

// transport capabilities matching plugin_api.h
const (
	transportCapText        uint32 = 1 << 0
	transportCapMedia       uint32 = 1 << 1
	transportCapReactions   uint32 = 1 << 2
	transportCapTyping      uint32 = 1 << 3
	transportCapEdit        uint32 = 1 << 4
	transportCapDelete      uint32 = 1 << 5
	transportCapReadReceipt uint32 = 1 << 6
	transportCapThreads     uint32 = 1 << 7
	transportCapVoice       uint32 = 1 << 8
)

// sc_transport_info_t method indices
const (
	transportCallText   = 0
	transportCallMedia  = 1
	transportCallReact  = 2
	transportCallRead   = 3
	transportCallTyping = 4
	transportCallEdit   = 5
	transportCallDelete = 6
	transportCallPoll   = 7
)

type workerTask struct {
	ev       transportEvent
	addons   []*LoadedAddon
	provider LLMProvider
	cfg      *Config
	log      *Logger
}

// InlineButton describes one button of an inline keyboard attached to a
// message. Data carries the raw base64 callback payload TDLib uses.
type InlineButton struct {
	Index    int    `json:"index,omitempty"`
	Row      int    `json:"row,omitempty"`
	Col      int    `json:"col,omitempty"`
	Text     string `json:"text"`
	Type     string `json:"type,omitempty"`
	Data     string `json:"data,omitempty"`
	DataText string `json:"data_text,omitempty"`
	URL      string `json:"url,omitempty"`
}

// PollOption is one answer option of a Telegram poll. Index is the 0-based
// option id used by the vote_poll tool.
type PollOption struct {
	Index      int    `json:"index"`
	Text       string `json:"text"`
	VoterCount int    `json:"voter_count,omitempty"`
	IsChosen   bool   `json:"is_chosen,omitempty"`
}

// Poll describes a Telegram poll or quiz attached to an incoming message.
type Poll struct {
	Question              string       `json:"question"`
	Options               []PollOption `json:"options"`
	TotalVoters           int          `json:"total_voter_count,omitempty"`
	IsAnonymous           bool         `json:"is_anonymous,omitempty"`
	AllowsMultipleAnswers bool         `json:"allows_multiple_answers,omitempty"`
	IsQuiz                bool         `json:"is_quiz,omitempty"`
	IsClosed              bool         `json:"is_closed,omitempty"`
}

// incoming transport event
type transportEvent struct {
	Type           string `json:"type"`
	Transport      string `json:"transport,omitempty"`
	ChatID         int64  `json:"chat_id"`
	SenderID       int64  `json:"sender_id"`
	SenderName     string `json:"sender_name,omitempty"`
	SenderUsername string `json:"sender_username,omitempty"`
	MessageID      int    `json:"message_id,omitempty"`
	ReplyToID      int    `json:"reply_to_id,omitempty"`
	ReplyFromSelf  bool   `json:"reply_from_self,omitempty"`
	SelfUsername   string `json:"self_username,omitempty"`
	SelfName       string `json:"self_name,omitempty"`
	SelfID         int64  `json:"self_id,omitempty"`
	TopicID        int    `json:"topic_id,omitempty"`
	Forum          bool   `json:"forum,omitempty"`
	Text           string `json:"text"`
	MediaType      string `json:"media_type,omitempty"`
	IsEdit         bool   `json:"is_edit,omitempty"`
	Date           int64  `json:"date,omitempty"`

	PhotoPath         string  `json:"photo_path,omitempty"`
	CustomEmojiIDs    []int64 `json:"custom_emoji_ids,omitempty"`
	StickerID         string  `json:"sticker_id,omitempty"`
	StickerEmoji      string  `json:"sticker_emoji,omitempty"`
	StickerAccessHash int64   `json:"sticker_access_hash,omitempty"`
	StickerFileRef    []byte  `json:"sticker_file_reference,omitempty"`
	StickerFileID     int     `json:"sticker_file_id,omitempty"`
	StickerAnimated   bool    `json:"sticker_animated,omitempty"`
	StickerThumbPath  string  `json:"sticker_thumb_path,omitempty"`
	StickerWidth      int     `json:"sticker_width,omitempty"`
	StickerHeight     int     `json:"sticker_height,omitempty"`

	// Inline bot attribution and inline keyboards.
	ViaBotID       int64          `json:"via_bot_id,omitempty"`
	ViaBotName     string         `json:"via_bot_name,omitempty"`
	ViaBotUsername string         `json:"via_bot_username,omitempty"`
	Buttons        []InlineButton `json:"buttons,omitempty"`

	// Poll/quiz attached to the message (media_type == "poll").
	Poll *Poll `json:"poll,omitempty"`

	// Callback query (a user pressed an inline button on one of our messages).
	CallbackQueryID  int64  `json:"callback_query_id,omitempty"`
	CallbackData     string `json:"callback_data,omitempty"`
	CallbackDataText string `json:"callback_data_text,omitempty"`
	ButtonText       string `json:"button_text,omitempty"`
	IsCallback       bool   `json:"-"`

	SemanticContext string `json:"-"`
	InlineContext   string `json:"-"`
	Activation      string `json:"-"`
}
