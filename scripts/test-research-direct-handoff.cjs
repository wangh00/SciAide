// Real production frontend; an in-memory owner, question, and candidate.
const assert = require('node:assert/strict');
const { setTimeout: sleep } = require('node:timers/promises');
const { deferred, openResearchBrowser } = require('./research-browser-fixture.cjs');

const projectId = 'handoff-project', taskId = 'handoff-task', conversationId = 'handoff-discussion';
const runId = 'discussion-run', callId = 'scope-question', candidateId = 'candidate-run', plannerId = 'planner-internal-chat';
const workflowId = 'candidate-workflow', versionId = 'candidate-version';
const secondTaskId = 'second-task', secondConversationId = 'second-discussion', secondIdea = '另一个任务：核对合成复习记录。';
const createdAt = '2026-11-22T12:00:00Z';
const idea = '比较合成练习的两种反馈，只讨论研究设计。';
const question = '只讨论设计，还是也拟定数据分析步骤？';
const answer = '只讨论设计，不执行。';
const reply = '已按回答保留原讨论，不启动正式科研。';
const profile = { id: 'fixture-profile', name: '本机浏览器夹具', modelId: 'fixture-model', enabled: true,
  isDefault: true, apiProtocol: 'openai_chat_completions', secretConfigured: true,
  models: [{ id: 'fixture-model', enabled: true, isDefault: true, contextWindowTokens: 128000, reasoningLevels: [] }] };
const layers = ['范围', '设计', '审查'].map((title, i) => ({ layerId: String(i), title, objective: '', stages: [] }));
const plan = { normalizedQuestion: idea, confidence: 'medium', recommendationReason: '仅准备讨论中的候选。',
  missingInformation: [], limitations: [], availableResources: [], resourceGaps: [], selectedSkills: [], recommendedRouteId: 'design',
  routes: [{ routeId: 'design', title: '只做科研设计的候选', reason: '保留未执行状态', validation: 'ready',
    availableNow: true, stagePlans: [{ stageId: 'research_design' }], stageIds: ['research_design'], layers, gaps: [],
    requiredResources: [], reviewCheckpoints: [], deliverables: ['研究方案'], blockers: [], planningNotes: [] }] };
const candidate = {
  run: { id: candidateId, projectId, researchTaskId: taskId, workflowId, workflowVersionId: versionId, conversationId: plannerId,
    workflowPurpose: 'research_starter', status: 'completed', createdAt, updatedAt: createdAt,
    inputs: { starter_context: { researchIdea: idea, discussionConversationId: conversationId } }, outputs: { plan }, compilation: {} },
  researchDiscussion: { projectId, researchTaskId: taskId, conversationId: plannerId, primaryConversationId: conversationId,
    sourceWorkflowRunId: candidateId, canExplore: true },
  steps: [], events: [], pendingApprovals: [], aiExecutions: [],
};
const workflow = { id: workflowId, projectId, name: '候选路线', purpose: 'research_starter', currentVersionId: versionId };
const definition = { schemaVersion: 1, name: '候选路线', nodes: [], edges: [], inputs: [], outputs: [] };

function fixture(failAt, twoTasks = false) {
  const calls = [], messages = [];
  const resume = deferred(), retryRead = deferred();
  const heldReads = new Map();
  const makeEntry = (taskId, conversationId, title) => ({ projectId, researchTaskId: taskId, conversationId, title, researchQuestion: title,
    createdAt, permissionMode: 'plan', canExplore: true, reasoningLevel: 'medium',
    modelProfileId: profile.id, modelId: profile.modelId, referenceAttachmentIds: [] });
  let entry = twoTasks ? makeEntry(taskId, conversationId, idea) : undefined;
  const second = twoTasks ? makeEntry(secondTaskId, secondConversationId, secondIdea) : undefined;
  let run, revision = 0, candidateAvailable = false, failureObserved = false;
  let candidateReadUnavailable = false, historyReadUnavailable = false;
  const entries = () => [entry, second].filter(Boolean);
  const asConversation = value => ({ ...value, id: value.conversationId, title: `Research: ${value.title}` });
  const session = () => candidateAvailable && candidateReadUnavailable ? { entry, currentRunUnavailable: true }
    : { entry, currentRun: candidateAvailable ? candidate : undefined };
  const snapshot = () => run ? { run, messages, sequence: revision, workflowAI: false, toolCalls: [], runSteps: [],
    pendingApprovals: [], userInput: run.status === 'waiting_user_input' ? {
      runId, callId, questions: [{ id: 'scope', question, required: true, selectionMode: 'single' }],
    } : undefined } : null;
  const view = value => ({ revision: String(revision), unchanged: false, value });
  const message = (id, role, status, text) => ({ id, runId, role, status, createdAt, parts: [{ type: 'text', text }] });
  const timeline = () => ({ taskId, latestRunId: candidateAvailable ? candidateId : '', conversationId, hasMore: false, entries: [
    ...messages.map((value, i) => ({ id: `message:${value.id}`, sequence: String(i + 1), kind: 'message', message: value,
      conversationId, active: false, createdAt, snapshot: {} })),
    ...(candidateAvailable ? [{ id: 'candidate-completion', sequence: '20', kind: 'event', eventType: 'workflow.completed',
      runId: candidateId, conversationId: plannerId, active: true, createdAt, snapshot: { event: {} } }] : []),
  ] });
  async function waitOnRead(name) {
    const hold = heldReads.get(name);
    if (!hold) return;
    hold.entered = true;
    await hold.promise;
    hold.returned = true;
  }
  async function maybeFail(read) {
    if (read !== failAt) return;
    if (!failureObserved) {
      failureObserved = true;
      throw Error(`Fixture: temporary ${read} read failure`);
    }
    await retryRead.promise;
  }
  async function call(facade, method, args) {
    const name = `${facade}.${method}`;
    calls.push({ name, args });
    switch (name) {
      case 'ProjectFacade.ListProjects':
        return [{ id: projectId, name: '候选切换隔离夹具', workspaceKind: 'external', workspacePath: 'D:/Fixture' }];
      case 'ModelFacade.ListModelProfiles': return [profile];
      case 'WorkflowFacade.StartResearchDiscussion':
        assert.equal(entry, undefined, 'must preserve the original task owner');
        assert.equal(args[0].researchIdea, idea);
        entry = makeEntry(taskId, conversationId, idea);
        return entry;
      case 'WorkflowFacade.GetRunByConversation': return { found: false };
      case 'WorkflowFacade.GetResearchTaskSessionByConversation':
        if (args[0] === secondConversationId && second) return { entry: second };
        assert.equal(args[0], conversationId);
        await waitOnRead('session');
        if (historyReadUnavailable) throw Error('Fixture: temporary research identity read failure');
        return session();
      case 'WorkflowFacade.GetResearchTaskSession':
        if (args[0] === projectId && args[1] === secondTaskId && second) return { entry: second };
        assert.deepEqual(args.slice(0, 2), [projectId, taskId]);
        await waitOnRead('task');
        return session();
      case 'WorkflowFacade.ListResearchTasks':
        return entries().map(value => ({ ...value, id: value.researchTaskId, status: 'active', originKind: 'ai_route' }));
      case 'WorkflowFacade.ListProjectRuns':
        return candidateAvailable ? [candidate.run] : [];
      case 'WorkflowFacade.GetPendingResearchAdoption':
        assert.deepEqual(args, [projectId, candidateId]);
        await maybeFail('adoption');
        return null;
      case 'WorkflowFacade.Get':
        assert.deepEqual(args, [projectId, workflowId]);
        await maybeFail('workflow');
        await waitOnRead('workflow');
        return { workflow, versions: [{ id: versionId, workflowId, version: 1, definition }] };
      case 'WorkflowFacade.GetCurrentResearchTaskRun': return candidate;
      case 'WorkflowFacade.PollRun':
        assert.deepEqual(args.slice(0, 2), [projectId, candidateId]);
        return view(candidate);
      case 'WorkflowFacade.GetResearchStarterPlan':
        assert.deepEqual(args, [projectId, candidateId]);
        return plan;
      case 'WorkflowFacade.PollResearchTimeline':
        return view(args[0].taskId === secondTaskId && second
          ? { taskId: secondTaskId, conversationId: secondConversationId, latestRunId: '', entries: [], hasMore: false } : timeline());
      case 'ConversationFacade.ListConversations': return entries().map(asConversation);
      case 'ConversationFacade.GetConversation': {
        const owner = entries().find(value => args[0] === value.conversationId);
        assert.ok(owner, 'candidate must not select its internal planner chat');
        if (args[0] === conversationId) await waitOnRead('conversation');
        return asConversation(owner);
      }
      case 'ConversationFacade.ListMessages': return args[0] === secondConversationId ? [] : messages;
      case 'ChatFacade.PollLatestRunSnapshot':
        if (args[0] === secondConversationId && second) return view(null);
        assert.equal(args[0], conversationId);
        return view(snapshot());
      case 'ChatFacade.PollRunSnapshot': return view(snapshot());
      case 'ChatFacade.StartChat':
        assert.equal(run, undefined);
        assert.equal(args[0].conversationId, conversationId);
        assert.equal(args[0].text, idea);
        run = { id: runId, conversationId, userMessageId: args[0].clientMessageId, assistantMessageId: 'assistant',
          modelProfileId: profile.id, modelId: profile.modelId, status: 'waiting_user_input', createdAt };
        messages.push(message(run.userMessageId, 'user', 'complete', idea), message(run.assistantMessageId, 'assistant', 'streaming', ''));
        revision++;
        return { ...run, status: 'queued' };
      case 'ChatFacade.RespondToUserInput':
        assert.deepEqual(args[0], { conversationId, runId, callId, answers: { scope: { text: answer } }, dismiss: false });
        await resume.promise;
        run.status = 'completed';
        messages[1].status = 'complete';
        messages[1].parts[0].text = reply;
        revision++;
        return snapshot();
      default:
        if (['Start', 'StartResearch', 'CreateConversation', 'AdoptResearchRoute', 'Retry'].includes(method)) {
          throw Error(`Unexpected Workflow or conversation mutation: ${name}`);
        }
        return method.startsWith('List') ? [] : null;
    }
  }
  return { call, calls, releaseAnswer: resume.resolve, releaseRetry: retryRead.resolve,
    publishCandidate() { candidateAvailable = true; revision++; }, failed: () => failureObserved,
    setCandidateReadUnavailable(value) { candidateReadUnavailable = value; },
    setHistoryReadUnavailable(value) { historyReadUnavailable = value; },
    holdRead(name) {
      const hold = { ...deferred(), entered: false, returned: false };
      heldReads.set(name, hold);
      return hold;
    } };
}

const writes = mock => mock.calls.filter(c => /\.(StartResearchDiscussion|StartResearch|StartChat|RespondToUserInput|Start|Retry|AdoptResearchRoute|CreateConversation)$/.test(c.name)).map(c => c.name);
const historyButton = (page, title) => page.locator('.conversation-list').getByRole('button', { name: `Research: ${title}`, exact: true });
const activeHistoryButton = (page, title) => page.locator('.conversation-list .conversation-row.active').getByRole('button', { name: `Research: ${title}`, exact: true });
const directHeading = (page, title) => page.locator('#direct-research-panel').getByRole('heading', { name: title, exact: true });
const candidateHeading = page => page.getByRole('heading', { name: '候选路线已准备', exact: true });
const settleReact = page => page.evaluate(() => new Promise(resolve => requestAnimationFrame(() => requestAnimationFrame(resolve))));

async function until(test, detail) {
  const deadline = Date.now() + 7000;
  while (Date.now() < deadline) {
    if (await test()) return;
    await sleep(25);
  }
  assert.fail(detail);
}

async function scenario(failAt) {
  const mock = fixture(failAt);
  const { page, browser, errors, open } = await openResearchBrowser(mock.call);
  const direct = page.locator('#direct-research-panel');
  const composer = page.locator('#workspace-message-input');
  const questionCard = page.locator('.user-input-card').filter({ hasText: question });
  const candidateHeading = page.getByRole('heading', { name: '候选路线已准备', exact: true });
  const lookupAttempts = () => mock.calls.filter(c => c.name === 'WorkflowFacade.GetPendingResearchAdoption').length;
  try {
    await open();
    await page.getByRole('button', { name: '科研模式', exact: true }).click();
    await composer.fill(idea);
    await page.getByRole('button', { name: '开始讨论', exact: true }).click();
    await questionCard.waitFor();
    await direct.waitFor();
    mock.publishCandidate();
    // One actual 1.5s refresh tick must not replace an awaiting discussion.
    await sleep(1700);
    assert.equal(lookupAttempts(), 0, 'a candidate opened while the original question was awaiting input');
    assert.equal(await questionCard.isVisible(), true);
    assert.equal(await direct.isVisible(), true);
    await questionCard.getByRole('textbox', { name: '你的回答或补充' }).fill(answer);
    await questionCard.getByRole('button', { name: '确定', exact: true }).click();
    await questionCard.getByRole('button', { name: '正在提交…', exact: true }).waitFor();
    assert.equal(lookupAttempts(), 0, 'a candidate opened during the answer submission');
    mock.releaseAnswer();
    await page.getByText(reply, { exact: true }).waitFor();
    await questionCard.waitFor({ state: 'hidden' });
    const waitUntil = Date.now() + 7000;
    while (!mock.failed() && Date.now() < waitUntil) await sleep(25);
    assert.ok(mock.failed(), 'the candidate open did not reach the controlled read failure');
    await page.evaluate(() => new Promise(resolve => requestAnimationFrame(() => requestAnimationFrame(resolve))));
    assert.equal(await direct.isVisible(), true, `a temporary ${failAt} read failure hid the original research discussion`);
    assert.equal(await candidateHeading.isVisible(), false);
    assert.equal(await page.getByRole('button', { name: '开始讨论', exact: true }).count(), 0);
    assert.equal(await page.getByText(reply, { exact: true }).isVisible(), true);
    await direct.getByText('暂时无法打开候选路线，原讨论已保留。', { exact: true }).waitFor();
    const draft = '这条尚未发送的讨论仍属于原会话。';
    await composer.fill(draft);

    mock.releaseRetry();
    await candidateHeading.waitFor();
    await direct.waitFor({ state: 'hidden' });
    assert.equal(await page.getByText(reply, { exact: true }).isVisible(), true);
    assert.equal(await composer.inputValue(), draft);
    assert.equal(await page.getByRole('button', { name: '发送', exact: true }).isEnabled(), true);
    assert.deepEqual(writes(mock), ['WorkflowFacade.StartResearchDiscussion', 'ChatFacade.StartChat', 'ChatFacade.RespondToUserInput']);
    assert.deepEqual(mock.calls.filter(c => c.name === 'ConversationFacade.GetConversation' && c.args[0] !== conversationId), []);
    assert.deepEqual(errors, []);
    console.log(`PASS: awaiting/answering on original Chat Run -> keep the task on ${failAt} read failure -> show the unadopted candidate`);
  } finally {
    mock.releaseAnswer();
    mock.releaseRetry();
    await browser.close();
  }
}

async function reopenAwaitingCandidate() {
  const mock = fixture();
  const { page, browser, errors, open } = await openResearchBrowser(mock.call);
  const waiting = page.locator('.user-input-card').filter({ hasText: question });
  const ownerRead = mock.holdRead('workflow');
  try {
    await open();
    await page.getByRole('button', { name: '科研模式', exact: true }).click();
    await page.locator('#workspace-message-input').fill(idea);
    await page.getByRole('button', { name: '开始讨论', exact: true }).click();
    await waiting.waitFor();
    await directHeading(page, idea).waitFor();
    mock.publishCandidate();
    mock.setCandidateReadUnavailable(true);
    await page.reload();
    await historyButton(page, idea).click();
    await waiting.waitFor();
    await until(async () => ownerRead.entered || await directHeading(page, idea).isVisible(), 'reopen never restored the task');
    assert.equal(ownerRead.entered, false, 'reopen tried to open the candidate before restoring the waiting discussion');
    assert.equal(await directHeading(page, idea).isVisible(), true);
    assert.equal(await candidateHeading(page).isVisible(), false);
    await page.locator('#direct-research-panel').getByRole('status').filter({hasText: '暂时无法读取候选运行，原讨论已保留。'}).waitFor();
    assert.deepEqual(writes(mock), ['WorkflowFacade.StartResearchDiscussion', 'ChatFacade.StartChat']);
    mock.setCandidateReadUnavailable(false);
    await waiting.getByRole('textbox', { name: '你的回答或补充' }).fill(answer);
    await waiting.getByRole('button', { name: '确定', exact: true }).click();
    mock.releaseAnswer();
    ownerRead.resolve();
    await candidateHeading(page).waitFor();
    await page.getByText(reply, { exact: true }).waitFor();
    assert.deepEqual(writes(mock), ['WorkflowFacade.StartResearchDiscussion', 'ChatFacade.StartChat', 'ChatFacade.RespondToUserInput']);
    assert.deepEqual(errors, []);
    console.log('PASS: a candidate read failure retained the original task and pending question after reopen; resume the same Run, no replay or adoption');
  } finally {
    mock.releaseAnswer();
    ownerRead.resolve();
    await browser.close();
  }
}

async function supersededRead(layer) {
  const mock = fixture(undefined, true);
  const { page, browser, errors, open } = await openResearchBrowser(mock.call);
  const held = mock.holdRead(layer);
  try {
    await open();
    if (layer === 'session') mock.publishCandidate();
    await historyButton(page, idea).click();
    if (layer === 'workflow') {
      await directHeading(page, idea).waitFor();
      mock.publishCandidate();
    }
    await until(() => held.entered, `did not reach the old ${layer} read`);
    await historyButton(page, secondIdea).click();
    await activeHistoryButton(page, secondIdea).waitFor();
    await until(() => directHeading(page, secondIdea).isVisible(), 'the new conversation did not replace the old task panel');
    held.resolve();
    await until(() => held.returned, `the old ${layer} read did not return`);
    await settleReact(page);
    assert.equal(await activeHistoryButton(page, secondIdea).isVisible(), true, `late ${layer} reply selected the old conversation`);
    assert.equal(await directHeading(page, secondIdea).isVisible(), true);
    assert.equal(await candidateHeading(page).isVisible(), false);
    assert.deepEqual(writes(mock), []);
    assert.deepEqual(errors, []);
    console.log(`PASS: late ${layer} reply cannot take over another selected research task`);
  } finally {
    held.resolve();
    await browser.close();
  }
}

async function competingEntries(first, last, layer = first === 'list' ? 'task' : 'session') {
  const mock = fixture(undefined, true);
  const held = mock.holdRead(layer);
  const { page, browser, errors, open } = await openResearchBrowser(mock.call);
  const button = (source, title) => source === 'history' ? historyButton(page, title)
    : page.locator('.workflow-project-tasks article button').filter({ hasText: title });
  try {
    await open();
    await page.getByRole('button', { name: '科研模式', exact: true }).click();
    await page.locator('.workflow-project-tasks').getByRole('heading', { name: '科研任务', exact: true }).waitFor();
    await button(first, idea).click();
    await until(() => held.entered, `did not reach the old ${first} request`);
    await button(last, secondIdea).click();
    await directHeading(page, secondIdea).waitFor();
    await activeHistoryButton(page, secondIdea).waitFor();
    held.resolve();
    await until(() => held.returned, 'the older task reply did not resolve');
    await settleReact(page);
    assert.equal(await directHeading(page, secondIdea).isVisible(), true, `${first} overrode the newer ${last} task panel`);
    assert.equal(await activeHistoryButton(page, secondIdea).isVisible(), true, `${first} overrode the newer ${last} conversation`);
    assert.deepEqual(writes(mock), []);
    assert.deepEqual(errors, []);
    console.log(`PASS: latest ${last} click wins over a delayed ${first} click (${layer} read)`);
  } finally {
    held.resolve();
    await browser.close();
  }
}

async function historyIdentityReadFailure() {
  const mock = fixture(undefined, true);
  const ordinary = { id: 'ordinary-chat', projectId, title: '普通对话夹具', createdAt };
  const { page, browser, errors, open } = await openResearchBrowser(async (facade, method, args) => {
    if (facade === 'WorkflowFacade' && method === 'GetResearchTaskSessionByConversation' && args[0] === ordinary.id) return null;
    const result = await mock.call(facade, method, args);
    return facade === 'ConversationFacade' && method === 'ListConversations' ? [...result, ordinary] : result;
  });
  try {
    await open();
    await historyButton(page, secondIdea).click();
    await directHeading(page, secondIdea).waitFor();
    mock.setHistoryReadUnavailable(true);
    await historyButton(page, idea).click();
    await page.getByText('暂时无法确认该会话的科研归属，已保留当前视图。请重试。', { exact: true }).waitFor();
    assert.equal(await directHeading(page, secondIdea).isVisible(), true);
    assert.equal(await activeHistoryButton(page, secondIdea).isVisible(), true);
    mock.setHistoryReadUnavailable(false);
    await historyButton(page, idea).click();
    await directHeading(page, idea).waitFor();
    await activeHistoryButton(page, idea).waitFor();
    await page.locator('.conversation-list').getByRole('button', { name: ordinary.title, exact: true }).click();
    await page.locator('.app-shell.mode-chat').waitFor();
    await page.locator('.conversation-list .conversation-row.active').getByRole('button', { name: ordinary.title, exact: true }).waitFor();
    assert.deepEqual(writes(mock), []);
    assert.deepEqual(errors, []);
    console.log('PASS: preserve the current view on identity error; retry restores research while a confirmed ordinary chat stays ordinary');
  } finally { await browser.close(); }
}

(async () => {
  for (const run of [
    () => scenario('adoption'), () => scenario('workflow'), reopenAwaitingCandidate,
    () => supersededRead('session'), () => supersededRead('workflow'),
    () => competingEntries('list', 'list'), () => competingEntries('list', 'history'),
    () => competingEntries('history', 'list'), () => competingEntries('list', 'history', 'conversation'), historyIdentityReadFailure,
  ]) {
    try { await run(); }
    catch (error) { console.error(error); process.exitCode = 1; }
  }
})().catch(error => { console.error(error); process.exitCode = 1; });
