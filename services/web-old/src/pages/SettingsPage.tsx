export function SettingsPage() {
  return (
    <div style={{ display: 'flex', flexDirection: 'column', height: '100%', overflow: 'hidden' }}>
      <div style={{ padding: '14px 20px', borderBottom: '1px solid var(--border)', flexShrink: 0 }}>
        <h1 style={{ margin: 0, fontSize: 18, fontWeight: 700, color: 'var(--text)', letterSpacing: '-0.02em' }}>
          Settings
        </h1>
      </div>
      <div style={{ flex: 1, overflow: 'auto', padding: '24px 20px' }}>
        <section style={{ maxWidth: 480 }}>
          <h2 style={{ margin: '0 0 4px', fontSize: 15, fontWeight: 700, color: 'var(--text)' }}>Profile</h2>
          <p style={{ margin: '0 0 20px', fontSize: 12, color: 'var(--text3)' }}>
            Manage your display name, email, and password.
          </p>
          <div style={{ padding: '32px 0', color: 'var(--text3)', fontSize: 13 }}>
            Profile settings coming soon.
          </div>
        </section>
      </div>
    </div>
  )
}
