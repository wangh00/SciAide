const baseline = [
  "本地项目与会话",
  "安全的模型配置",
  "可审计 Agent 工具调用",
  "MCP 与 Skill 扩展",
  "科研知识库与可信引用",
] as const;

export default function App() {
  return (
    <main className="shell">
      <header className="hero">
        <div className="brand">SB</div>
        <div>
          <p className="eyebrow">科研智能体桌面平台</p>
          <h1>SciAide</h1>
          <p className="subtitle">P0 工程基线已就绪，下一步配置你的第一个模型。</p>
        </div>
      </header>

      <section className="panel" aria-labelledby="baseline-title">
        <div>
          <p className="step">PHASE 0</p>
          <h2 id="baseline-title">从可靠的基础开始</h2>
          <p>所有能力将沿着明确的权限、持久化和恢复边界逐步加入。</p>
        </div>
        <ol>
          {baseline.map((item, index) => (
            <li key={item}>
              <span>{String(index + 1).padStart(2, "0")}</span>
              {item}
            </li>
          ))}
        </ol>
      </section>
    </main>
  );
}
