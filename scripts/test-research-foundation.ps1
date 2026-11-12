# Fast, reproducible acceptance gate for the shared research execution chain.
# Uses temporary databases and scripted HTTP models, never the user's tasks.
# This supplements, not replaces, CI's complete go test ./... suite.
$ErrorActionPreference = 'Stop'
Set-StrictMode -Version Latest
$root = Split-Path -Parent $PSScriptRoot
$previous = @{ GOOS=$env:GOOS; GOARCH=$env:GOARCH; CGO_ENABLED=$env:CGO_ENABLED }
function Invoke-Check([string]$Command, [string[]]$Arguments) {
    & $Command @Arguments
    if ($LASTEXITCODE -ne 0) { throw "Acceptance check failed: $Command $($Arguments -join ' ')" }
}
Push-Location $root
try {
    $env:GOOS='windows'; $env:GOARCH='amd64'; $env:CGO_ENABLED='0'
    $packages = @('./internal/app/reviewcontract','./internal/app/reportview','./internal/app/workflow','./internal/app/agent','./internal/app/researchworkflow','./internal/app/artifact','./internal/tools/builtin')
    Invoke-Check 'go' (@('test') + $packages + @('-count=1'))
    $tests = @(
        'TestResearchOpeningDiscussesThenPreparesWithoutExecuting',
        'TestCitationFirstSubmissionHTTPAcrossProtocols',
        'TestLiteratureProfileRealAdaptersHTTPAndSQLitePreservePlan',
        'TestRouteDeliveryPreservesMaterialsThroughRealRuntime',
        'TestDynamicEmpiricalResearchRouteCompletesAndRegistersReviewedReport',
        'TestTabularAuditPreservesDirectRepairAndScientificConfirmation',
        'TestReviewConvergenceAcrossProtocols',
        'TestWideReviewNavigationHTTPRestartRepairAndGateAcrossProtocols',
        'TestFailedV1ReviewExplicitRetryCreatesV2WithoutReplayingPriorWork',
        'TestInheritedDebugModelWorkflowRetryPreservesTaskAndLocalRepair',
        'TestReviewFixtureVisibleScalarText',
        'TestReviewLocalRepairAcrossProtocolsPreservesRejectionAndUpstream',
        'TestReviewQuestionResumesSameAttemptBeforeDirectRepair',
        'TestIndependentReviewConcurrencyRetryPreservesPriorStagesAndDelivery',
        'TestReviewRepairScopeReportOmissionKeepsMethodAndRealMethodErrorReopensIt',
        'TestLargeGateTechnicalRecoveryThenOriginalRevisionAcrossProtocols',
        'TestRepeatedSourcesHTTPReadRestartRetryAndFullSubmission',
        'TestSourceSharingGrowingReportRepairReviewAndGate',
        'TestSeparatedReportPublicationRetryAndExportsSurviveRestart'
    )
    Invoke-Check 'go' @('test','./internal/bootstrap','-run',('^(' + ($tests -join '|') + ')$'),'-count=1','-timeout','15m')
    Invoke-Check 'npm' @('--prefix','frontend','run','typecheck')
    Invoke-Check 'npm' @('--prefix','frontend','test')
    Write-Host 'Research foundation automated acceptance passed. Real-model scientific acceptance remains separate.'
} finally {
    $env:GOOS=$previous.GOOS; $env:GOARCH=$previous.GOARCH; $env:CGO_ENABLED=$previous.CGO_ENABLED
    Pop-Location
}
