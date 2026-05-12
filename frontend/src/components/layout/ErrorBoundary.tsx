import { Component, type ReactNode } from 'react';
import { Button } from '../ui/button';
import { AlertTriangle } from 'lucide-react';

interface Props {
  children: ReactNode;
}

interface State {
  error: Error | null;
}

export class ErrorBoundary extends Component<Props, State> {
  state: State = { error: null };

  static getDerivedStateFromError(error: Error): State {
    return { error };
  }

  render() {
    if (this.state.error) {
      return (
        <div className="flex h-full items-center justify-center">
          <div className="max-w-md space-y-4 rounded-lg border bg-card p-8 text-center">
            <AlertTriangle className="mx-auto h-8 w-8 text-destructive" />
            <h2 className="text-lg font-semibold">Something went wrong</h2>
            <p className="text-sm text-muted-foreground">{this.state.error.message}</p>
            <Button onClick={() => this.setState({ error: null })}>Try Again</Button>
          </div>
        </div>
      );
    }
    return this.props.children;
  }
}
