package server

import (
	"context"

	"github.com/vnovick/itervox/internal/domain"
)

// FuncClient moved here from server.go (CORE-110): it is test-only.

// FuncClient builds an OrchestratorClient from individual function fields.
// Any nil field falls back to the noopClient default. Intended for tests.
type FuncClient struct {
	FetchIssuesFn                     func(context.Context) ([]TrackerIssue, error)
	CancelIssueFn                     func(string) error
	ResumeIssueFn                     func(string) error
	TerminateIssueFn                  func(string) error
	ReanalyzeIssueFn                  func(string) error
	FetchLogsFn                       func(context.Context, string) []string
	GetSinceFn                        func(context.Context, string, uint32, int64, bool) ([]string, uint32, int64, bool)
	ClearLogsFn                       func(string) error
	ClearAllLogsFn                    func() error
	ClearIssueSubLogsFn               func(string) error
	ClearSessionSublogFn              func(string, string) error
	DispatchReviewerFn                func(string) error
	CommentOnIssueFn                  func(context.Context, string, string) error
	PostOperatorCommentFn             func(context.Context, string, string) (bool, error)
	CreateIssueFn                     func(context.Context, string, string, string, string) (*domain.Issue, error)
	UpdateIssueStateFn                func(context.Context, string, string) error
	SetWorkersFn                      func(int) error
	BumpWorkersFn                     func(int) (int, error)
	SetMaxRetriesFn                   func(int) error
	MaxRetriesFn                      func() int
	SetFailedStateFn                  func(string) error
	FailedStateFn                     func() string
	SetMaxSwitchesPerIssuePerWindowFn func(int) error
	MaxSwitchesPerIssuePerWindowFn    func() int
	SetSwitchWindowHoursFn            func(int) error
	SwitchWindowHoursFn               func() int
	SetIssueProfileFn                 func(string, string)
	SetIssueBackendFn                 func(string, string)
	CheckIssueBackendPinFn            func(string, string) error
	ClearBackendBreakerFn             func(string, string) bool
	ProfileDefsFn                     func() map[string]ProfileDef
	DefaultAgentCommandFn             func() string
	AvailableModelsFn                 func() map[string][]ModelOption
	ReviewerConfigFn                  func() (string, bool)
	SetReviewerConfigFn               func(string, bool) error
	UpsertProfileFn                   func(string, ProfileDef, string) error
	DeleteProfileFn                   func(string) error
	SetAutomationsFn                  func([]AutomationDef) error
	SetAutoClearWorkspaceFn           func(bool) error
	SetDepsAnalysisModeFn             func(string) error
	ClearAllWorkspacesFn              func() error
	FetchLogIdentifiersFn             func() []string
	UpdateTrackerStatesFn             func([]string, []string, string) error
	FetchSubLogsFn                    func(context.Context, string) ([]domain.IssueLogEntry, error)
	AddSSHHostFn                      func(string, string) error
	RemoveSSHHostFn                   func(string) error
	SetDispatchStrategyFn             func(string) error
	SetInlineInputFn                  func(bool) error
	ProvideInputFn                    func(string, string) error
	DismissInputFn                    func(string) error
	BumpCommentCountFn                func(string)
	TestAutomationFn                  func(context.Context, string, string) error
	SetDepsOverrideFn                 func(string, bool) bool
	RetryOutboxEntryFn                func(string) bool
	DropOutboxEntryFn                 func(string)
}

func (c *FuncClient) FetchIssues(ctx context.Context) ([]TrackerIssue, error) {
	if c.FetchIssuesFn != nil {
		return c.FetchIssuesFn(ctx)
	}
	return nil, errNotConfigured
}
func (c *FuncClient) CancelIssue(id string) error {
	if c.CancelIssueFn != nil {
		return c.CancelIssueFn(id)
	}
	return errNotConfigured
}
func (c *FuncClient) ResumeIssue(id string) error {
	if c.ResumeIssueFn != nil {
		return c.ResumeIssueFn(id)
	}
	return errNotConfigured
}
func (c *FuncClient) TerminateIssue(id string) error {
	if c.TerminateIssueFn != nil {
		return c.TerminateIssueFn(id)
	}
	return errNotConfigured
}
func (c *FuncClient) ReanalyzeIssue(id string) error {
	if c.ReanalyzeIssueFn != nil {
		return c.ReanalyzeIssueFn(id)
	}
	return errNotConfigured
}
func (c *FuncClient) FetchLogs(ctx context.Context, id string) []string {
	if c.FetchLogsFn != nil {
		return c.FetchLogsFn(ctx, id)
	}
	return nil
}
func (c *FuncClient) GetSince(ctx context.Context, id string, epoch uint32, cursor int64, hasCursor bool) ([]string, uint32, int64, bool) {
	if c.GetSinceFn != nil {
		return c.GetSinceFn(ctx, id, epoch, cursor, hasCursor)
	}
	return nil, 0, 0, false
}
func (c *FuncClient) ClearLogs(id string) error {
	if c.ClearLogsFn != nil {
		return c.ClearLogsFn(id)
	}
	return errNotConfigured
}
func (c *FuncClient) ClearAllLogs() error {
	if c.ClearAllLogsFn != nil {
		return c.ClearAllLogsFn()
	}
	return errNotConfigured
}
func (c *FuncClient) ClearIssueSubLogs(id string) error {
	if c.ClearIssueSubLogsFn != nil {
		return c.ClearIssueSubLogsFn(id)
	}
	return errNotConfigured
}
func (c *FuncClient) ClearSessionSublog(id, sessionID string) error {
	if c.ClearSessionSublogFn != nil {
		return c.ClearSessionSublogFn(id, sessionID)
	}
	return errNotConfigured
}
func (c *FuncClient) FetchSubLogs(ctx context.Context, id string) ([]domain.IssueLogEntry, error) {
	if c.FetchSubLogsFn != nil {
		return c.FetchSubLogsFn(ctx, id)
	}
	return nil, nil
}
func (c *FuncClient) DispatchReviewer(id string) error {
	if c.DispatchReviewerFn != nil {
		return c.DispatchReviewerFn(id)
	}
	return errNotConfigured
}
func (c *FuncClient) CommentOnIssue(ctx context.Context, identifier, body string) error {
	if c.CommentOnIssueFn != nil {
		return c.CommentOnIssueFn(ctx, identifier, body)
	}
	return errNotConfigured
}
func (c *FuncClient) PostOperatorComment(ctx context.Context, identifier, body string) (bool, error) {
	if c.PostOperatorCommentFn != nil {
		return c.PostOperatorCommentFn(ctx, identifier, body)
	}
	return false, errNotConfigured
}
func (c *FuncClient) CreateIssue(ctx context.Context, identifier, title, body, state string) (*domain.Issue, error) {
	if c.CreateIssueFn != nil {
		return c.CreateIssueFn(ctx, identifier, title, body, state)
	}
	return nil, errNotConfigured
}
func (c *FuncClient) UpdateIssueState(ctx context.Context, id, state string) error {
	if c.UpdateIssueStateFn != nil {
		return c.UpdateIssueStateFn(ctx, id, state)
	}
	return errNotConfigured
}
func (c *FuncClient) SetWorkers(n int) error {
	if c.SetWorkersFn != nil {
		return c.SetWorkersFn(n)
	}
	return nil
}
func (c *FuncClient) BumpWorkers(delta int) (int, error) {
	if c.BumpWorkersFn != nil {
		return c.BumpWorkersFn(delta)
	}
	return 0, nil
}
func (c *FuncClient) SetMaxRetries(n int) error {
	if c.SetMaxRetriesFn != nil {
		return c.SetMaxRetriesFn(n)
	}
	return nil
}
func (c *FuncClient) MaxRetries() int {
	if c.MaxRetriesFn != nil {
		return c.MaxRetriesFn()
	}
	return 0
}
func (c *FuncClient) SetFailedState(s string) error {
	if c.SetFailedStateFn != nil {
		return c.SetFailedStateFn(s)
	}
	return nil
}
func (c *FuncClient) FailedState() string {
	if c.FailedStateFn != nil {
		return c.FailedStateFn()
	}
	return ""
}
func (c *FuncClient) SetMaxSwitchesPerIssuePerWindow(n int) error {
	if c.SetMaxSwitchesPerIssuePerWindowFn != nil {
		return c.SetMaxSwitchesPerIssuePerWindowFn(n)
	}
	return nil
}
func (c *FuncClient) MaxSwitchesPerIssuePerWindow() int {
	if c.MaxSwitchesPerIssuePerWindowFn != nil {
		return c.MaxSwitchesPerIssuePerWindowFn()
	}
	return 0
}
func (c *FuncClient) SetSwitchWindowHours(h int) error {
	if c.SetSwitchWindowHoursFn != nil {
		return c.SetSwitchWindowHoursFn(h)
	}
	return nil
}
func (c *FuncClient) SwitchWindowHours() int {
	if c.SwitchWindowHoursFn != nil {
		return c.SwitchWindowHoursFn()
	}
	return 0
}
func (c *FuncClient) SetIssueProfile(id, profile string) {
	if c.SetIssueProfileFn != nil {
		c.SetIssueProfileFn(id, profile)
	}
}

// ClearBackendBreaker implements BackendBreakerClearer; false when unset.
func (c *FuncClient) ClearBackendBreaker(backend, host string) bool {
	if c.ClearBackendBreakerFn != nil {
		return c.ClearBackendBreakerFn(backend, host)
	}
	return false
}

// CheckIssueBackendPin implements IssueBackendPinChecker; nil when unset.
func (c *FuncClient) CheckIssueBackendPin(id, backend string) error {
	if c.CheckIssueBackendPinFn != nil {
		return c.CheckIssueBackendPinFn(id, backend)
	}
	return nil
}

func (c *FuncClient) SetIssueBackend(id, backend string) {
	if c.SetIssueBackendFn != nil {
		c.SetIssueBackendFn(id, backend)
	}
}
func (c *FuncClient) ProfileDefs() map[string]ProfileDef {
	if c.ProfileDefsFn != nil {
		return c.ProfileDefsFn()
	}
	return nil
}
func (c *FuncClient) DefaultAgentCommand() string {
	if c.DefaultAgentCommandFn != nil {
		return c.DefaultAgentCommandFn()
	}
	return ""
}
func (c *FuncClient) AvailableModels() map[string][]ModelOption {
	if c.AvailableModelsFn != nil {
		return c.AvailableModelsFn()
	}
	return nil
}
func (c *FuncClient) ReviewerConfig() (string, bool) {
	if c.ReviewerConfigFn != nil {
		return c.ReviewerConfigFn()
	}
	return "", false
}
func (c *FuncClient) SetReviewerConfig(profile string, autoReview bool) error {
	if c.SetReviewerConfigFn != nil {
		return c.SetReviewerConfigFn(profile, autoReview)
	}
	return nil
}
func (c *FuncClient) UpsertProfile(name string, def ProfileDef, originalName string) error {
	if c.UpsertProfileFn != nil {
		return c.UpsertProfileFn(name, def, originalName)
	}
	return errNotConfigured
}
func (c *FuncClient) DeleteProfile(name string) error {
	if c.DeleteProfileFn != nil {
		return c.DeleteProfileFn(name)
	}
	return errNotConfigured
}
func (c *FuncClient) SetAutomations(automations []AutomationDef) error {
	if c.SetAutomationsFn != nil {
		return c.SetAutomationsFn(automations)
	}
	return errNotConfigured
}
func (c *FuncClient) SetAutoClearWorkspace(enabled bool) error {
	if c.SetAutoClearWorkspaceFn != nil {
		return c.SetAutoClearWorkspaceFn(enabled)
	}
	return errNotConfigured
}
func (c *FuncClient) SetDepsAnalysisMode(mode string) error {
	if c.SetDepsAnalysisModeFn != nil {
		return c.SetDepsAnalysisModeFn(mode)
	}
	return errNotConfigured
}
func (c *FuncClient) ClearAllWorkspaces() error {
	if c.ClearAllWorkspacesFn != nil {
		return c.ClearAllWorkspacesFn()
	}
	return errNotConfigured
}
func (c *FuncClient) FetchLogIdentifiers() []string {
	if c.FetchLogIdentifiersFn != nil {
		return c.FetchLogIdentifiersFn()
	}
	return nil
}
func (c *FuncClient) UpdateTrackerStates(active, terminal []string, completion string) error {
	if c.UpdateTrackerStatesFn != nil {
		return c.UpdateTrackerStatesFn(active, terminal, completion)
	}
	return errNotConfigured
}
func (c *FuncClient) AddSSHHost(host, description string) error {
	if c.AddSSHHostFn != nil {
		return c.AddSSHHostFn(host, description)
	}
	return errNotConfigured
}
func (c *FuncClient) RemoveSSHHost(host string) error {
	if c.RemoveSSHHostFn != nil {
		return c.RemoveSSHHostFn(host)
	}
	return errNotConfigured
}
func (c *FuncClient) SetDispatchStrategy(strategy string) error {
	if c.SetDispatchStrategyFn != nil {
		return c.SetDispatchStrategyFn(strategy)
	}
	return errNotConfigured
}

// ProvideInput and DismissInput default to nil (queued), not errNotConfigured
// — matching noopClient, since these two never report "not found" (CORE-005).
func (c *FuncClient) ProvideInput(identifier, message string) error {
	if c.ProvideInputFn != nil {
		return c.ProvideInputFn(identifier, message)
	}
	return nil
}
func (c *FuncClient) DismissInput(identifier string) error {
	if c.DismissInputFn != nil {
		return c.DismissInputFn(identifier)
	}
	return nil
}
func (c *FuncClient) SetInlineInput(enabled bool) error {
	if c.SetInlineInputFn != nil {
		return c.SetInlineInputFn(enabled)
	}
	return errNotConfigured
}
func (c *FuncClient) BumpCommentCount(identifier string) {
	if c.BumpCommentCountFn != nil {
		c.BumpCommentCountFn(identifier)
	}
}
func (c *FuncClient) TestAutomation(ctx context.Context, automationID, identifier string) error {
	if c.TestAutomationFn != nil {
		return c.TestAutomationFn(ctx, automationID, identifier)
	}
	return errNotConfigured
}

func (c *FuncClient) SetDepsOverride(identifier string, enabled bool) bool {
	if c.SetDepsOverrideFn != nil {
		return c.SetDepsOverrideFn(identifier, enabled)
	}
	return false
}

func (c *FuncClient) RetryOutboxEntry(id string) bool {
	if c.RetryOutboxEntryFn != nil {
		return c.RetryOutboxEntryFn(id)
	}
	return false
}

func (c *FuncClient) DropOutboxEntry(id string) {
	if c.DropOutboxEntryFn != nil {
		c.DropOutboxEntryFn(id)
	}
}
