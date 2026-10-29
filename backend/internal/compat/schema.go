package compat

import "github.com/one-search/one-search/backend/internal/model"

type TavilySearchRequest struct {
	Query             string   `json:"query"`
	SearchDepth       string   `json:"search_depth,omitempty"`
	Topic             string   `json:"topic,omitempty"`
	Days              int      `json:"days,omitempty"`
	MaxResults        int      `json:"max_results,omitempty"`
	IncludeAnswer     bool     `json:"include_answer,omitempty"`
	IncludeRawContent bool     `json:"include_raw_content,omitempty"`
	IncludeImages     bool     `json:"include_images,omitempty"`
	IncludeDomains    []string `json:"include_domains,omitempty"`
	ExcludeDomains    []string `json:"exclude_domains,omitempty"`
	Providers         []string `json:"providers,omitempty"`
	Mode              string   `json:"mode,omitempty"`
	Cache             string   `json:"cache,omitempty"`
}

type TavilySearchResponse struct {
	Query        string         `json:"query"`
	Answer       string         `json:"answer,omitempty"`
	Results      []TavilyResult `json:"results"`
	ResponseTime float64        `json:"response_time"`
	RequestID    string         `json:"request_id"`
}

type TavilyResult struct {
	Title      string  `json:"title"`
	URL        string  `json:"url"`
	Content    string  `json:"content"`
	RawContent string  `json:"raw_content,omitempty"`
	Score      float64 `json:"score"`
}

type SerperSearchRequest struct {
	Q         string   `json:"q"`
	Num       int      `json:"num,omitempty"`
	Page      int      `json:"page,omitempty"`
	TBS       string   `json:"tbs,omitempty"`
	Gl        string   `json:"gl,omitempty"`
	Hl        string   `json:"hl,omitempty"`
	Providers []string `json:"providers,omitempty"`
	Mode      string   `json:"mode,omitempty"`
	Cache     string   `json:"cache,omitempty"`
}

type SerperSearchResponse struct {
	SearchParameters map[string]interface{} `json:"searchParameters"`
	Organic          []SerperOrganicResult  `json:"organic"`
	Credits          int                    `json:"credits,omitempty"`
	RequestID        string                 `json:"requestId"`
}

type SerperOrganicResult struct {
	Title    string `json:"title"`
	Link     string `json:"link"`
	Snippet  string `json:"snippet"`
	Position int    `json:"position"`
}

type OpenAISearchRequest struct {
	Query     string   `json:"query,omitempty"`
	Input     string   `json:"input,omitempty"`
	Providers []string `json:"providers,omitempty"`
	Mode      string   `json:"mode,omitempty"`
	Limit     int      `json:"limit,omitempty"`
	Cache     string   `json:"cache,omitempty"`
}

type OpenAISearchResponse struct {
	ID            string                   `json:"id"`
	Object        string                   `json:"object"`
	Status        string                   `json:"status"`
	SearchResults []model.SearchResult     `json:"search_results"`
	Output        []map[string]interface{} `json:"output"`
}

// DeepSeekSearchRequest 是 DeepSeek POST /v1/tools/search 的请求体。
//
// 字段与官方文档逐一对应：count 用指针区分「未传」与「传了 0」，后者必须报 400；
// ori_question 允许为 null，因此同样用指针。providers / mode / cache 不是官方字段，
// 是本服务附加的扩展，官方客户端不会传，传了也能用。
type DeepSeekSearchRequest struct {
	Query        string   `json:"query"`
	Count        *int     `json:"count,omitempty"`
	OriQuestion  *string  `json:"ori_question,omitempty"`
	Localization string   `json:"localization,omitempty"`
	NeedContent  bool     `json:"need_content,omitempty"`
	Providers    []string `json:"providers,omitempty"`
	Mode         string   `json:"mode,omitempty"`
	Cache        string   `json:"cache,omitempty"`
}

// DeepSeekSearchResponse 是 DeepSeek 搜索接口的成功响应。
// results 无匹配时必须是空数组而不是 null，构造时用 make 保证这一点。
type DeepSeekSearchResponse struct {
	Results []DeepSeekSearchResult `json:"results"`
}

// DeepSeekSearchResult 是单条搜索结果。
//
// site_name / site_icon / date 在官方契约里允许 null，因此用指针而不是 omitempty：
// 字段必须始终出现，缺失时显式输出 null。
type DeepSeekSearchResult struct {
	RerankScore float64 `json:"rerank_score"`
	Title       string  `json:"title"`
	URL         string  `json:"url"`
	Snippet     string  `json:"snippet"`
	Summary     string  `json:"summary"`
	Content     string  `json:"content"`
	SiteName    *string `json:"site_name"`
	SiteIcon    *string `json:"site_icon"`
	Date        *string `json:"date"`
	Authority   int     `json:"authority"`
}

// DeepSeekFetchRequest 是 DeepSeek POST /v1/tools/fetch 的请求体。
// 官方只接受 url 一个字段，未知字段按官方文档忽略。
type DeepSeekFetchRequest struct {
	URL string `json:"url"`
}

// DeepSeekFetchResponse 是 DeepSeek 抓取接口的成功响应。
// title 缺失时官方约定返回空字符串而不是省略字段。
type DeepSeekFetchResponse struct {
	Title   string `json:"title"`
	Content string `json:"content"`
}
