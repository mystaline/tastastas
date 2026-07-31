import { Component } from 'react'

interface Props { children: React.ReactNode }
interface State { error: Error | null }

export class ErrorBoundary extends Component<Props, State> {
  state: State = { error: null }

  static getDerivedStateFromError(error: Error): State {
    return { error }
  }

  render() {
    if (this.state.error) {
      return (
        <div className="flex items-center justify-center h-screen bg-slate-950 text-slate-200 p-8">
          <div className="text-center max-w-lg">
            <p className="text-red-400 font-mono text-sm mb-4 break-all">
              {this.state.error.message}
            </p>
            <button
              onClick={() => this.setState({ error: null })}
              className="px-4 py-2 bg-cyan-700 hover:bg-cyan-600 rounded text-sm cursor-pointer"
            >
              Retry
            </button>
          </div>
        </div>
      )
    }
    return this.props.children
  }
}
