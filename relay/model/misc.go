package model

type Usage struct {
	PromptTokens     int `json:"prompt_tokens"`
	CompletionTokens int `json:"completion_tokens"`
	TotalTokens      int `json:"total_tokens"`

	// A missing cache field is unknown, rather than zero: retail cache discounts require actual provider usage.
	PromptTokensDetails     *PromptTokensDetails     `json:"prompt_tokens_details,omitempty"`
	PromptCacheHitTokens    *int                     `json:"prompt_cache_hit_tokens,omitempty"`
	Estimated               bool                     `json:"-"`
	CompletionTokensDetails *CompletionTokensDetails `json:"completion_tokens_details,omitempty"`
}

type PromptTokensDetails struct {
	CachedTokens *int `json:"cached_tokens,omitempty"`
}

func (u *Usage) CachedInput() (int, bool) {
	cached, known := 0, false
	if u.PromptTokensDetails != nil && u.PromptTokensDetails.CachedTokens != nil {
		cached, known = *u.PromptTokensDetails.CachedTokens, true
	}
	if u.PromptCacheHitTokens != nil {
		if known && cached != *u.PromptCacheHitTokens {
			return 0, false
		}
		cached, known = *u.PromptCacheHitTokens, true
	}
	if cached < 0 || cached > u.PromptTokens || u.Estimated {
		return 0, false
	}
	return cached, known
}

type CompletionTokensDetails struct {
	ReasoningTokens          int `json:"reasoning_tokens"`
	AcceptedPredictionTokens int `json:"accepted_prediction_tokens"`
	RejectedPredictionTokens int `json:"rejected_prediction_tokens"`
}

type Error struct {
	Message string `json:"message"`
	Type    string `json:"type"`
	Param   string `json:"param"`
	Code    any    `json:"code"`
}

type ErrorWithStatusCode struct {
	Error
	StatusCode int `json:"status_code"`
}
