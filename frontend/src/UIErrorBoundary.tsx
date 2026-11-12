import { Component, ErrorInfo, ReactNode } from "react";

export function reportUIError(scope: string, error: unknown, info?: ErrorInfo) {
  // Only structural diagnostics: no message/report text or user materials.
  const kind = error instanceof Error ? error.name : "Error";
  const report = window.go?.wails?.SystemFacade?.ReportUIError;
  void report?.(scope, kind, info?.componentStack?.slice(0, 6000) ?? "").catch(() => undefined);
}

export class UIErrorBoundary extends Component<{children: ReactNode; scope: string; resetKey?: string}, {failed: boolean}> {
  override state = { failed: false };
  static getDerivedStateFromError() { return { failed: true }; }
  override componentDidCatch(error: Error, info: ErrorInfo) { reportUIError(this.props.scope, error, info); }
  override componentDidUpdate(previous: Readonly<{children: ReactNode; scope: string; resetKey?: string}>) {
    if (previous.resetKey !== this.props.resetKey && this.state.failed) this.setState({failed: false});
  }
  override render() {
    if (this.state.failed) return <section className="ui-recovery" role="alert">
      <h2>页面暂时无法显示</h2><p>这不代表科研任务失败。已保存的记录仍保留，重新显示不会重跑任务。</p>
      <button type="button" onClick={() => this.setState({failed: false})}>重新显示页面</button>
      <button type="button" onClick={() => window.location.reload()}>重新加载界面</button>
    </section>;
    return this.props.children;
  }
}
