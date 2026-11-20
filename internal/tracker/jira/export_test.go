package jira

// SetMaxPages lowers the page cap so a test can reach it fast.
func SetMaxPages(c *Client, n int) { c.maxPages = n }

// ADFToText exposes adfToText to package tests.
func ADFToText(v any) string { return adfToText(v) }

// SetCommentPageSize lowers the comment page size so a test can span pages.
func (c *Client) SetCommentPageSize(n int) { c.commentPageSize = n }

// TextToADF exposes textToADF to package tests.
func TextToADF(text string) map[string]any { return textToADF(text) }

// PlainTextToADF exposes plainTextToADF to package tests.
func PlainTextToADF(text string) map[string]any { return plainTextToADF(text) }
