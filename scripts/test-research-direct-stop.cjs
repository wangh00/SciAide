// Production frontend bundle, with an in-memory Wails Facade and event fixture.
// Does not run the native window, touch a database, or call a model.
const assert = require('node:assert/strict');
const { deferred, openResearchBrowser } = require('./research-browser-fixture.cjs');

const projectId = 'stop-fixture-project';
const taskId = 'stop-fixture-task';
const conversationId = 'stop-fixture-discussion';
const createdAt = '2026-11-22T12:00:00Z';
const idea = '比较两种合成学习反馈，只讨论研究设计。';
const early = '这是停止前已经收到的片段。';
const late = '不应展示停止后送达的片段。';
const lateFinal = '不应把取消回合渲染成最终回复。';
const follow = '请继续讨论如何设置公平的对照。';
const answer = '后续讨论仍属于原任务。';
const profile = {
  id: 'fixture-profile', name: '本机浏览器夹具', modelId: 'fixture-model', enabled: true, isDefault: true,
  apiProtocol: 'openai_chat_completions', secretConfigured: true,
  models: [{ id: 'fixture-model', enabled: true, isDefault: true, contextWindowTokens: 128000, reasoningLevels: [] }],
};

function fixture() {
  const calls = [], messages = [], runs = [];
  const readAfterStop = deferred();
  let entry, revision = 0;
  const latest = () => runs.at(-1);
  const snapshot = run => run && ({
    run, messages, sequence: revision, workflowAI: false, toolCalls: [], runSteps: [], pendingApprovals: [],
  });
  const view = value => ({ revision: String(revision), unchanged: false, value });
  const timeline = () => ({
    taskId, latestRunId: '', conversationId, hasMore: false,
    entries: messages.map((message, index) => ({
      id: `message:${message.id}`, kind: 'message', conversationId, sequence: String(index + 1),
      active: false, createdAt: message.createdAt, snapshot: {}, message,
    })),
  });
  const conversation = () => ({ ...entry, id: conversationId, title: `Research: ${entry.title}`, updatedAt: createdAt });
  const stoppingRead = async read => {
    if (latest()?.status === 'cancelled') await readAfterStop.promise;
    return read();
  };
  async function call(facade, method, args) {
    const name = `${facade}.${method}`;
    calls.push({ name, args });
    switch (name) {
      case 'ProjectFacade.ListProjects':
        return [{ id: projectId, name: '直接科研停止夹具', workspaceKind: 'external', workspacePath: 'D:/Fixture' }];
      case 'ModelFacade.ListModelProfiles':
        return [profile];
      case 'WorkflowFacade.StartResearchDiscussion':
        assert.equal(entry, undefined, 'first discussion must create only one research task');
        assert.equal(args[0].projectId, projectId);
        assert.equal(args[0].researchIdea, idea);
        assert.equal('discussionOnly' in args[0], false, 'no compatibility starter flag');
        entry = {
          projectId, researchTaskId: taskId, conversationId, title: idea, researchQuestion: idea, createdAt,
          permissionMode: 'plan', canExplore: true, reasoningLevel: 'medium',
          modelProfileId: profile.id, modelId: profile.modelId, referenceAttachmentIds: [],
        };
        return entry;
      case 'WorkflowFacade.ListResearchTasks':
        return entry ? [{ ...entry, id: taskId, status: 'active', originKind: 'ai_route' }] : [];
      case 'WorkflowFacade.ListProjectRuns':
        return []; // Chat turns are not candidate/Workflow Runs.
      case 'WorkflowFacade.GetRunByConversation':
        return { found: false };
      case 'WorkflowFacade.GetResearchTaskSessionByConversation':
        assert.equal(args[0], conversationId);
        return { entry };
      case 'WorkflowFacade.GetResearchTaskSession':
        assert.deepEqual(args.slice(0, 2), [projectId, taskId]);
        return { entry };
      case 'ConversationFacade.ListConversations':
        return entry ? [conversation()] : [];
      case 'ConversationFacade.GetConversation':
        assert.equal(args[0], conversationId);
        return conversation();
      case 'ConversationFacade.ListMessages':
        return stoppingRead(() => messages);
      case 'WorkflowFacade.PollResearchTimeline':
        return stoppingRead(() => view(timeline()));
      case 'ChatFacade.PollLatestRunSnapshot':
        return stoppingRead(() => view(snapshot(latest()) ?? null));
      case 'ChatFacade.PollRunSnapshot':
        return stoppingRead(() => view(snapshot(runs.find(run => run.id === args[0]))));
      case 'ChatFacade.StartChat': {
        const command = args[0];
        const first = runs.length === 0;
        assert.equal(command.conversationId, conversationId);
        assert.equal(command.text, first ? idea : follow);
        assert.ok(command.clientMessageId);
        const id = `chat-${runs.length + 1}`;
        const at = first ? createdAt : '2026-11-22T12:01:00Z';
        const run = {
          id, conversationId, userMessageId: command.clientMessageId, assistantMessageId: `assistant-${id}`,
          modelProfileId: profile.id, modelId: profile.modelId, status: first ? 'running' : 'completed', createdAt: at,
        };
        messages.push(
          { id: run.userMessageId, runId: id, role: 'user', status: 'complete', parts: [{ type: 'text', text: command.text }], createdAt: at },
          { id: run.assistantMessageId, runId: id, role: 'assistant', status: first ? 'streaming' : 'complete',
            parts: [{ type: 'text', text: first ? early : answer }], createdAt: at },
        );
        runs.push(run);
        revision++;
        return run;
      }
      case 'ChatFacade.CancelRun': {
        const run = latest();
        assert.equal(run.status, 'running');
        assert.equal(args[0], run.id);
        run.status = 'cancelled';
        const assistant = messages.find(message => message.id === run.assistantMessageId);
        assistant.status = 'incomplete';
        assistant.parts[0].text = early;
        revision = 3;
        return null;
      }
      default:
        if (['Start', 'StartResearch', 'CreateConversation', 'AdoptResearchRoute', 'Retry'].includes(method)) {
          throw Error(`Unexpected Workflow/lifecycle fallback: ${name}`);
        }
        if (method.startsWith('List')) return [];
        return null;
    }
  }
  return { calls, runs, call, allowReads: readAfterStop.resolve };
}

async function main() {
  const mock = fixture();
  const { page, browser, errors, open } = await openResearchBrowser(mock.call);
  try {
    const button = name => page.getByRole('button', { name, exact: true });
    const composer = page.locator('#workspace-message-input');
    await open();
    await button('科研模式').click();
    await composer.fill(idea);
    await button('开始讨论').click();
    await button('停止生成').waitFor();
    const { id: aggregateId, assistantMessageId: messageId } = mock.runs[0];
    const delta = (sequence, text) => ({ aggregateId, sequence, type: 'content.delta', payload: { messageId, delta: text } });
    await page.getByText(early, { exact: true }).waitFor();

    await button('停止生成').click();
    const deadline = Date.now() + 7000;
    while (mock.runs[0].status !== 'cancelled' && Date.now() < deadline) await new Promise(resolve => setTimeout(resolve, 20));
    assert.equal(mock.runs[0].status, 'cancelled', 'Stop must reach the original Chat Run');
    // The cancel snapshot is still held. In production an already-dispatched
    // visual batch may arrive with a later event sequence than the stop event.
    await page.evaluate(events => {
      for (const event of events) window.fixtureRunEvent(event);
    }, [{ aggregateId, sequence: 3, type: 'run.cancelled', payload: { runId: aggregateId } }, delta(4, late)]);
    await button('发送').waitFor();
    await page.evaluate(() => new Promise(resolve => requestAnimationFrame(() => requestAnimationFrame(resolve))));
    const visibleDelta = await page.getByText(late, { exact: false }).count();
    await page.evaluate(event => window.fixtureRunEvent(event), {
      aggregateId, sequence: 5, type: 'content.completed', payload: { messageId, text: lateFinal },
    });
    await page.evaluate(() => new Promise(resolve => requestAnimationFrame(resolve)));
    const visibleCompletion = await page.getByText(lateFinal, { exact: false }).count();
    assert.deepEqual({ visibleDelta, visibleCompletion }, { visibleDelta: 0, visibleCompletion: 0 },
      'delayed frames repainted the cancelled reply while the terminal snapshot was held');
    await page.getByText(early, { exact: true }).waitFor();

    mock.allowReads();
    const writes = () => mock.calls.filter(call => /\.(StartResearchDiscussion|StartResearch|StartChat|CancelRun|Start|Retry|AdoptResearchRoute|CreateConversation)$/.test(call.name));
    const beforeReload = writes().length;
    await page.reload();
    const history = page.locator('.conversation-list').getByRole('button', { name: `Research: ${idea}`, exact: true });
    await history.click();
    await page.locator('.app-shell.mode-research').waitFor();
    await page.getByText(early, { exact: true }).waitFor();
    await button('发送').waitFor();
    assert.equal(await button('停止生成').count(), 0, 'reopen must not reanimate the stopped Chat Run');
    assert.equal(writes().length, beforeReload, 'reopen must not send or create another task');
    await page.evaluate(event => window.fixtureRunEvent(event), delta(6, late));
    await page.evaluate(() => new Promise(resolve => requestAnimationFrame(() => requestAnimationFrame(resolve))));
    assert.equal(await page.getByText(late, { exact: false }).count(), 0, 'reopened cancelled Run accepted a delayed frame');
    await composer.fill(follow);
    await button('发送').click();
    await page.getByText(answer, { exact: true }).waitFor();
    assert.deepEqual(writes().map(call => call.name), [
      'WorkflowFacade.StartResearchDiscussion', 'ChatFacade.StartChat', 'ChatFacade.CancelRun', 'ChatFacade.StartChat',
    ]);
    assert.deepEqual(mock.runs.map(run => [run.conversationId, run.status]), [
      [conversationId, 'cancelled'], [conversationId, 'completed'],
    ]);
    assert.deepEqual(errors, []);
    console.log('PASS: direct discussion -> visible streaming -> stop -> reject delayed frames -> reopen -> explicit follow-up, no Workflow or model calls');
  } finally {
    mock.allowReads();
    await browser.close();
  }
}

main().catch(error => { console.error(error); process.exitCode = 1; });
