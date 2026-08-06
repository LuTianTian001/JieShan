import { AlertTriangle, RefreshCw } from 'lucide-react';
import { Component, type ErrorInfo, type ReactNode } from 'react';
import { Button } from '../../components/ui';

interface Props {
  children: ReactNode;
  resetKey?: string;
}

interface State {
  error: Error | null;
}

export class UpstreamErrorBoundary extends Component<Props, State> {
  state: State = { error: null };

  static getDerivedStateFromError(error: Error): State {
    return { error };
  }

  componentDidCatch(error: Error, info: ErrorInfo) {
    console.error('Upstream page crashed', error, info);
  }

  componentDidUpdate(previous: Props) {
    if (this.state.error && previous.resetKey !== this.props.resetKey) {
      this.setState({ error: null });
    }
  }

  render() {
    if (!this.state.error) return this.props.children;
    return (
      <div className="upstream-crash-state" role="alert">
        <AlertTriangle size={24} aria-hidden="true" />
        <div>
          <strong>页面显示失败</strong>
          <p>{this.state.error.message || '上游页面遇到了无法恢复的显示错误。'}</p>
        </div>
        <Button icon={RefreshCw} onClick={() => window.location.reload()}>重新加载</Button>
      </div>
    );
  }
}
