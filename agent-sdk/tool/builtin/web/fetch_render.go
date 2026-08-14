package web

import (
	"bytes"
	"net/url"
	"strings"

	readability "codeberg.org/readeck/go-readability/v2"
	htmltomarkdown "github.com/JohannesKaufmann/html-to-markdown/v2"
	"github.com/JohannesKaufmann/html-to-markdown/v2/converter"
	"github.com/microcosm-cc/bluemonday"
)

func (t *FetchTool) renderResponse(resp fetchResponseMeta, body []byte, format string) map[string]any {
	payload := map[string]any{
		"status":       "completed",
		"url":          resp.requestURL,
		"final_url":    resp.finalURL,
		"content_type": resp.contentType,
		"format":       format,
		"status_code":  resp.statusCode,
	}
	if !isTextualMime(resp.mimeType) {
		payload["status"] = "unsupported"
		payload["message"] = "WebFetch only returns text, markdown, or HTML content in this version."
		return payload
	}
	content := string(body)
	title := ""
	if isHTMLMime(resp.mimeType) {
		rendered, renderedTitle := renderHTMLContent(content, resp.finalURL, format)
		content = rendered
		title = renderedTitle
	} else if format == "html" {
		content = bluemonday.UGCPolicy().Sanitize(content)
	}
	putNonEmpty(payload, "title", title)
	payload["content"] = content
	return payload
}

func renderHTMLContent(input string, finalURL string, format string) (string, string) {
	pageURL, _ := url.Parse(finalURL)
	article, err := readability.FromReader(strings.NewReader(input), pageURL)
	if err == nil && article.Node != nil {
		var buf bytes.Buffer
		switch format {
		case "text":
			if article.RenderText(&buf) == nil {
				return strings.TrimSpace(buf.String()), strings.TrimSpace(article.Title())
			}
		default:
			if article.RenderHTML(&buf) == nil {
				html := bluemonday.UGCPolicy().Sanitize(buf.String())
				if format == "html" {
					return strings.TrimSpace(html), strings.TrimSpace(article.Title())
				}
				if markdown, err := htmltomarkdown.ConvertString(html, converter.WithDomain(originForMarkdown(finalURL))); err == nil {
					return strings.TrimSpace(markdown), strings.TrimSpace(article.Title())
				}
			}
		}
	}
	sanitized := bluemonday.UGCPolicy().Sanitize(input)
	switch format {
	case "html":
		return strings.TrimSpace(sanitized), ""
	case "text":
		return strings.TrimSpace(bluemonday.StrictPolicy().Sanitize(input)), ""
	default:
		if markdown, err := htmltomarkdown.ConvertString(sanitized, converter.WithDomain(originForMarkdown(finalURL))); err == nil {
			return strings.TrimSpace(markdown), ""
		}
		return strings.TrimSpace(bluemonday.StrictPolicy().Sanitize(input)), ""
	}
}

func isTextualMime(mimeType string) bool {
	mimeType = strings.ToLower(strings.TrimSpace(mimeType))
	return mimeType == "" ||
		strings.HasPrefix(mimeType, "text/") ||
		mimeType == "application/xhtml+xml" ||
		mimeType == "application/xml" ||
		mimeType == "application/json"
}

func isHTMLMime(mimeType string) bool {
	mimeType = strings.ToLower(strings.TrimSpace(mimeType))
	return mimeType == "text/html" || mimeType == "application/xhtml+xml"
}

func originForMarkdown(raw string) string {
	u, err := url.Parse(raw)
	if err != nil || u.Scheme == "" || u.Host == "" {
		return raw
	}
	return u.Scheme + "://" + u.Host
}
