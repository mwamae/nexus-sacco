import { Component, type ReactNode } from 'react';

type FallbackRender = (error: Error, retry: () => void) => ReactNode;

type Props = {
  children: ReactNode;
  fallback: FallbackRender;
};

type State = { error: Error | null };

// Class-based error boundary — there is no hook equivalent. Used to
// stop a thrown render-time error in one page from unmounting the
// AppShell + nuking AuthContext on its way down. See App.tsx for the
// Notifications wiring that motivated this.
export class ErrorBoundary extends Component<Props, State> {
  state: State = { error: null };

  static getDerivedStateFromError(error: Error): State {
    return { error };
  }

  componentDidCatch(error: Error) {
    // Surface to devtools — production telemetry would hook in here.
    if (typeof console !== 'undefined') {
      console.error('ErrorBoundary caught:', error);
    }
  }

  reset = () => this.setState({ error: null });

  render() {
    if (this.state.error) {
      return this.props.fallback(this.state.error, this.reset);
    }
    return this.props.children;
  }
}
