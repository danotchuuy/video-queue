import react from '@vitejs/plugin-react'
import { defineConfig } from 'vite'

// https://vite.dev/config/
export default defineConfig({
  plugins: [react()],
  server: {
    port: 5173,
    // Listen on all interfaces (not just localhost) so other devices on the
    // network can reach the dev server. Dev-only; don't expose to an
    // untrusted network.
    host: true,
    // Vite rejects Host headers other than localhost or an IP (DNS rebinding
    // guard). Allow any name so the machine can be reached by hostname.
    allowedHosts: true,
    // Forward API calls to the Go server during development.
    proxy: {
      '/api': 'http://localhost:8080',
    },
  },
})
