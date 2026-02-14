package main

import (
	"context"
	"crypto/tls"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	"github.com/PuerkitoBio/goquery"
	"github.com/mark3labs/mcp-go/mcp"
	"github.com/mark3labs/mcp-go/server"
	"golang.org/x/time/rate"
)

type SearchResult struct {
	Title    string
	Link     string
	Snippet  string
	Position int
}

type DoHResponse struct {
	Answer []struct {
		Data string `json:"data"`
		Type int    `json:"type"`
	} `json:"Answer"`
}

var (
	dnsCache = make(map[string]string)
	dnsMutex sync.RWMutex
)

func resolveOverDoH(ctx context.Context, domain string) (string, error) {
	tr := &http.Transport{
		TLSClientConfig: &tls.Config{InsecureSkipVerify: true},
	}
	client := &http.Client{Transport: tr, Timeout: 5 * time.Second}
	req, err := http.NewRequestWithContext(ctx, "GET", fmt.Sprintf("https://1.1.1.1/dns-query?name=%s&type=A", domain), nil)
	if err != nil {
		return "", err
	}
	req.Header.Set("Accept", "application/dns-json")
	resp, err := client.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("DoH status: %d", resp.StatusCode)
	}
	var doh DoHResponse
	if err := json.NewDecoder(resp.Body).Decode(&doh); err != nil {
		return "", err
	}
	for _, answer := range doh.Answer {
		if answer.Type == 1 {
			return answer.Data, nil
		}
	}
	return "", fmt.Errorf("no A record: %s", domain)
}

func newAntiCensorshipClient() *http.Client {
	return &http.Client{
		Timeout: 30 * time.Second,
		Transport: &http.Transport{
			TLSClientConfig: &tls.Config{InsecureSkipVerify: false},
			DialContext: func(ctx context.Context, network, addr string) (net.Conn, error) {
				host, port, err := net.SplitHostPort(addr)
				if err != nil {
					return nil, err
				}
				dnsMutex.RLock()
				ip, found := dnsCache[host]
				dnsMutex.RUnlock()
				if !found {
					if strings.Contains(host, "duckduckgo.com") {
						resolvedIP, err := resolveOverDoH(ctx, host)
						if err == nil {
							ip = resolvedIP
							dnsMutex.Lock()
							dnsCache[host] = ip
							dnsMutex.Unlock()
						} else {
							ip = host
						}
					} else {
						ip = host
					}
				}
				dialer := &net.Dialer{Timeout: 30 * time.Second, KeepAlive: 30 * time.Second}
				return dialer.DialContext(ctx, network, net.JoinHostPort(ip, port))
			},
		},
		CheckRedirect: func(req *http.Request, via []*http.Request) error {
			if len(via) >= 10 {
				return fmt.Errorf("too many redirects")
			}
			return nil
		},
	}
}

type DuckDuckGoSearcher struct {
	limiter *rate.Limiter
	client  *http.Client
}

func NewDuckDuckGoSearcher() *DuckDuckGoSearcher {
	return &DuckDuckGoSearcher{
		limiter: rate.NewLimiter(rate.Every(time.Second), 1),
		client:  newAntiCensorshipClient(),
	}
}

func (s *DuckDuckGoSearcher) FormatResultsForLLM(query string, results []SearchResult) string {
	if len(results) == 0 {
		return fmt.Sprintf("# GoDuckDuckGo Search Results\n\nNo results found for query: \"%s\"", query)
	}
	var sb strings.Builder
	sb.WriteString(fmt.Sprintf("# GoDuckDuckGo Search Results\n\nFound %d results for: \"%s\"\n\n---\n\n", len(results), query))
	for _, result := range results {
		sb.WriteString(fmt.Sprintf("### %s\n", result.Title))
		sb.WriteString(fmt.Sprintf("%s\n\n", result.Snippet))
		sb.WriteString(fmt.Sprintf("🔗 [Read More](%s)\n\n", result.Link))
	}
	return sb.String()
}

func (s *DuckDuckGoSearcher) Search(ctx context.Context, query string, maxResults int, safeSearch string) ([]SearchResult, error) {
	if err := s.limiter.Wait(ctx); err != nil {
		return nil, fmt.Errorf("rate limit: %w", err)
	}
	kp := "-1"
	switch strings.ToLower(safeSearch) {
	case "strict":
		kp = "1"
	case "off":
		kp = "-2"
	}
	form := url.Values{}
	form.Set("q", query)
	form.Set("b", "")
	form.Set("kl", "")
	form.Set("kp", kp)
	req, err := http.NewRequestWithContext(ctx, "POST", "https://html.duckduckgo.com/html", strings.NewReader(form.Encode()))
	if err != nil {
		return nil, err
	}
	req.Header.Set("User-Agent", "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/91.0.4472.124 Safari/537.36")
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	resp, err := s.client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("status: %d", resp.StatusCode)
	}
	doc, err := goquery.NewDocumentFromReader(resp.Body)
	if err != nil {
		return nil, err
	}
	var results []SearchResult
	doc.Find(".result").EachWithBreak(func(i int, sel *goquery.Selection) bool {
		titleElem := sel.Find(".result__title")
		if titleElem.Length() == 0 {
			return true
		}
		linkElem := titleElem.Find("a")
		if linkElem.Length() == 0 {
			return true
		}
		title := strings.TrimSpace(linkElem.Text())
		link, exists := linkElem.Attr("href")
		if !exists {
			return true
		}
		if strings.Contains(link, "y.js") {
			return true
		}
		if strings.HasPrefix(link, "//duckduckgo.com/l/?uddg=") {
			parts := strings.Split(link, "uddg=")
			if len(parts) > 1 {
				decoded, err := url.QueryUnescape(strings.Split(parts[1], "&")[0])
				if err == nil {
					link = decoded
				}
			}
		}
		snippetElem := sel.Find(".result__snippet")
		snippet := ""
		if snippetElem.Length() > 0 {
			snippet = strings.TrimSpace(snippetElem.Text())
		}
		results = append(results, SearchResult{Title: title, Link: link, Snippet: snippet, Position: len(results) + 1})
		return len(results) < maxResults
	})
	return results, nil
}

type WebContentFetcher struct {
	limiter *rate.Limiter
	client  *http.Client
}

func NewWebContentFetcher() *WebContentFetcher {
	return &WebContentFetcher{
		limiter: rate.NewLimiter(rate.Every(time.Minute/20), 1),
		client:  newAntiCensorshipClient(),
	}
}

func (f *WebContentFetcher) FetchAndParse(ctx context.Context, urlStr string) (string, error) {
	if err := f.limiter.Wait(ctx); err != nil {
		return "", fmt.Errorf("rate limit: %w", err)
	}
	req, err := http.NewRequestWithContext(ctx, "GET", urlStr, nil)
	if err != nil {
		return "", err
	}
	req.Header.Set("User-Agent", "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/91.0.4472.124 Safari/537.36")
	resp, err := f.client.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("status: %d", resp.StatusCode)
	}
	doc, err := goquery.NewDocumentFromReader(resp.Body)
	if err != nil {
		return "", err
	}
	doc.Find("script, style, nav, header, footer").Remove()
	text := doc.Text()
	lines := strings.Split(text, "\\n")
	var cleanLines []string
	for _, line := range lines {
		line = strings.TrimSpace(line)
		if line != "" {
			line = strings.Join(strings.Fields(line), " ")
			cleanLines = append(cleanLines, line)
		}
	}
	text = strings.Join(cleanLines, " ")
	if len(text) > 8000 {
		text = text[:8000] + "... [truncated]"
	}
	return fmt.Sprintf("Successfully fetched and parsed content (%d characters):\\n%s", len(text), text), nil
}

func main() {
	searcher := NewDuckDuckGoSearcher()
	fetcher := NewWebContentFetcher()
	s := server.NewMCPServer("GoDuckDuckGo", "1.0.2", server.WithLogging())
	s.AddTool(mcp.NewTool("search",
		mcp.WithDescription("Search DuckDuckGo and return formatted results. Ideal for general queries, news, articles, and online content."),
		mcp.WithString("query", mcp.Description("The search query string")),
		mcp.WithNumber("max_results", mcp.Description("Maximum number of results to return (default: 10)")),
		mcp.WithString("safe_search", mcp.Description("SafeSearch level: 'strict', 'moderate', or 'off' (default: 'moderate')"), mcp.Enum("strict", "moderate", "off")),
	), func(ctx context.Context, request mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		query := mcp.ParseString(request, "query", "")
		maxResults := int(mcp.ParseInt(request, "max_results", 10))
		safeSearch := mcp.ParseString(request, "safe_search", "moderate")
		results, err := searcher.Search(ctx, query, maxResults, safeSearch)
		if err != nil {
			return mcp.NewToolResultError(fmt.Sprintf("An error occurred while searching: %v", err)), nil
		}
		return mcp.NewToolResultText(searcher.FormatResultsForLLM(query, results)), nil
	})
	s.AddTool(mcp.NewTool("fetch_content",
		mcp.WithDescription("Fetch and parse content from a webpage URL"),
		mcp.WithString("url", mcp.Description("The webpage URL to fetch content from")),
	), func(ctx context.Context, request mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		urlStr := mcp.ParseString(request, "url", "")
		content, err := fetcher.FetchAndParse(ctx, urlStr)
		if err != nil {
			return mcp.NewToolResultError(fmt.Sprintf("An error occurred while fetching content: %v", err)), nil
		}
		return mcp.NewToolResultText(content), nil
	})
	if err := server.ServeStdio(s); err != nil {
		fmt.Printf("Server error: %v\\n", err)
	}
}
