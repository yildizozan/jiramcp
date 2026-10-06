package mcpserver

import (
	"context"
	"fmt"

	"github.com/mark3labs/mcp-go/mcp"
)

// defaultCommentLimit is how many of the newest comments list_comments returns
// when the caller does not ask for a specific number.
const defaultCommentLimit = 20

// addCommentTool appends a comment to an existing issue.
func addCommentTool() mcp.Tool {
	return mcp.NewTool("add_comment",
		mcp.WithDescription("Add a comment to an existing Jira issue."),
		mcp.WithString("key", mcp.Required(), mcp.Description("Issue key, e.g. DOSD-1036.")),
		mcp.WithString("body", mcp.Required(), mcp.Description("Comment text (plain text; converted to ADF on Cloud)."), mcp.MaxLength(32000)),
	)
}

func (s *Server) handleAddComment(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	key, err := req.RequireString("key")
	if err != nil {
		return mcp.NewToolResultError("key is required"), nil
	}
	key, err = s.requireMappedIssue(ctx, key)
	if err != nil {
		return mcp.NewToolResultError(err.Error()), nil
	}
	body, err := req.RequireString("body")
	if err != nil {
		return mcp.NewToolResultError("body is required"), nil
	}
	c, err := s.client.AddComment(ctx, key, body)
	if err != nil {
		return mcp.NewToolResultError(err.Error()), nil
	}
	s.logger.Info("comment added", "key", key, "commentId", c.ID)
	return mcp.NewToolResultStructured(
		map[string]any{"key": key, "commentId": c.ID, "url": c.URL},
		fmt.Sprintf("Commented on %s: %s", key, c.URL)), nil
}

// listCommentsTool reads the newest comments on an issue. It exists mainly so
// update_comment is usable: a comment can only be edited by id, and the id is
// otherwise invisible to the caller.
func listCommentsTool() mcp.Tool {
	return mcp.NewTool("list_comments",
		mcp.WithDescription("List the most recent comments on a Jira issue, oldest first. Use the returned `id` to edit a comment with update_comment."),
		mcp.WithString("key", mcp.Required(), mcp.Description("Issue key, e.g. DOSD-1036.")),
		mcp.WithNumber("limit", mcp.Description("How many of the newest comments to return (default 20)."), mcp.Min(1), mcp.Max(100)),
	)
}

func (s *Server) handleListComments(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	key, err := req.RequireString("key")
	if err != nil {
		return mcp.NewToolResultError("key is required"), nil
	}
	key, err = s.requireMappedIssue(ctx, key)
	if err != nil {
		return mcp.NewToolResultError(err.Error()), nil
	}
	limit := req.GetInt("limit", defaultCommentLimit)
	comments, err := s.client.ListComments(ctx, key, limit)
	if err != nil {
		return mcp.NewToolResultError(err.Error()), nil
	}
	return mcp.NewToolResultStructured(
		map[string]any{"key": key, "comments": comments},
		fmt.Sprintf("found %d comment(s) on %s", len(comments), key)), nil
}

// updateCommentTool edits an existing comment.
func updateCommentTool() mcp.Tool {
	return mcp.NewTool("update_comment",
		mcp.WithDescription("Edit an existing comment on a Jira issue. The body REPLACES the current text; it is not appended. Get `comment_id` from list_comments or from the add_comment result. Editing someone else's comment requires the service account to hold Jira's 'Edit All Comments' permission; without it Jira rejects the edit."),
		mcp.WithString("key", mcp.Required(), mcp.Description("Issue key, e.g. DOSD-1036.")),
		mcp.WithString("comment_id", mcp.Required(), mcp.Description("Id of the comment to edit, e.g. 9001.")),
		mcp.WithString("body", mcp.Required(), mcp.Description("New comment text, replacing the old one (plain text; converted to ADF on Cloud)."), mcp.MaxLength(32000)),
	)
}

func (s *Server) handleUpdateComment(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	key, err := req.RequireString("key")
	if err != nil {
		return mcp.NewToolResultError("key is required"), nil
	}
	key, err = s.requireMappedIssue(ctx, key)
	if err != nil {
		return mcp.NewToolResultError(err.Error()), nil
	}
	commentID, err := req.RequireString("comment_id")
	if err != nil {
		return mcp.NewToolResultError("comment_id is required"), nil
	}
	body, err := req.RequireString("body")
	if err != nil {
		return mcp.NewToolResultError("body is required"), nil
	}
	c, err := s.client.UpdateComment(ctx, key, commentID, body)
	if err != nil {
		return mcp.NewToolResultError(err.Error()), nil
	}
	s.logger.Info("comment updated", "key", key, "commentId", c.ID)
	return mcp.NewToolResultStructured(
		map[string]any{"key": key, "commentId": c.ID, "url": c.URL},
		fmt.Sprintf("Updated comment %s on %s: %s", c.ID, key, c.URL)), nil
}
