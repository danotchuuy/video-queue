import { useSearchParams } from 'react-router'

const MESSAGES: Record<string, string> = {
  discord: 'Something went wrong talking to Discord. Please try again.',
  cancelled: 'Sign-in was cancelled.',
}

export default function Login() {
  const [params] = useSearchParams()
  const code = params.get('error')
  const message = code ? (MESSAGES[code] ?? 'Sign-in failed. Please try again.') : null
  return (
    <main className="container login">
      <h1>Video Queue</h1>
      <p className="muted">Sign in to manage your video queues.</p>
      {message && (
        <div className="error" role="alert">
          {message}
        </div>
      )}
      <a className="button primary" href="/api/auth/login">
        Sign in with Discord
      </a>
    </main>
  )
}
