package workflow

// RetryGuard binds a reset to the durable state that was actually checked.
// It is internal persistence input, not a UI authorization or model argument.
// The repository must compare it inside the SAME transaction as the reset.
type RetryGuard struct {
	RunID               string
	StepID              string
	RunStatus           RunStatus
	CurrentStep         int
	CancelRequested     bool
	ResumeStatus        RunStatus
	InputsSHA256        string
	CompilationSHA256   string
	StepStatus          StepStatus
	Attempt             int
	InputSHA256         string
	EventSequence       int64
	ProducerStepID      string
	ProducerAttempt     int
	ProducerInputSHA256 string
}

func NewRetryGuard(detail RunDetail, step Step, producer *Step) RetryGuard {
	g := RetryGuard{RunID: detail.Run.ID, StepID: step.ID, RunStatus: detail.Run.Status,
		CurrentStep: detail.Run.CurrentStep, CancelRequested: detail.Run.CancelRequested,
		ResumeStatus: detail.Run.ResumeStatus, InputsSHA256: detail.Run.InputsSHA256,
		CompilationSHA256: detail.Run.CompilationSHA256, StepStatus: step.Status,
		Attempt: step.Attempt, InputSHA256: step.InputSHA256, EventSequence: detail.RetryEventSequence}
	if producer != nil {
		g.ProducerStepID, g.ProducerAttempt, g.ProducerInputSHA256 = producer.ID, producer.Attempt, producer.InputSHA256
	}
	return g
}
