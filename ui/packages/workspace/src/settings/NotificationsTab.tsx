import { useEffect, useState } from 'react'
import { useQuery } from '@tanstack/react-query'
import { Send } from 'lucide-react'
import { notificationApi } from '@brokoli/api'
import { useSession } from '@brokoli/auth'
import { Badge, Button, Callout, ConfirmDialog, Field, Input, Spinner, errorMessage, useToast } from '@brokoli/ui'

/*
 * Slack and Teams webhooks. The server stores them, but on an open-source
 * server nothing reads them to deliver run alerts: that is done by a
 * notifier extension. The test message is the only sender in core, and it
 * covers Slack only. The page says so rather than showing "Active".
 */
export function NotificationsTab() {
  const session = useSession()
  const toast = useToast()
  const settings = useQuery({ queryKey: ['settings', 'notifications'], queryFn: notificationApi.get })
  const [channel, setChannel] = useState('')
  const [botName, setBotName] = useState('')
  const [slackUrl, setSlackUrl] = useState('')
  const [teamsUrl, setTeamsUrl] = useState('')
  const [busy, setBusy] = useState<'slack' | 'teams' | 'test' | null>(null)
  const [testResult, setTestResult] = useState<{ ok: boolean; text: string } | null>(null)
  const [clearing, setClearing] = useState(false)
  const canEdit = session.can('settings.edit')

  useEffect(() => {
    if (!settings.data) return
    setChannel(settings.data.channel ?? '')
    setBotName(settings.data.username || 'Brokoli')
  }, [settings.data])

  if (settings.isPending)
    return (
      <p className="ws-inline">
        <Spinner size="sm" /> Loading
      </p>
    )
  if (settings.isError) return <Callout tone="danger" title="Notification settings could not be loaded">{errorMessage(settings.error)}</Callout>
  const s = settings.data

  // Every save carries channel and bot name: the server blanks them when they are absent.
  const save = async (which: 'slack' | 'teams') => {
    setBusy(which)
    try {
      await notificationApi.update({
        channel,
        username: botName,
        ...(which === 'slack' && slackUrl ? { webhook: slackUrl } : {}),
        ...(which === 'teams' && teamsUrl ? { teams_webhook: teamsUrl } : {}),
      })
      toast.success(which === 'slack' ? 'Slack settings saved' : 'Teams webhook saved')
      which === 'slack' ? setSlackUrl('') : setTeamsUrl('')
      await settings.refetch()
    } catch (e) {
      toast.error('Not saved', e)
    } finally {
      setBusy(null)
    }
  }

  const test = async () => {
    setBusy('test')
    setTestResult(null)
    try {
      await notificationApi.test()
      setTestResult({ ok: true, text: 'Slack accepted the test message. Check the channel.' })
    } catch (e) {
      setTestResult({ ok: false, text: errorMessage(e) })
    } finally {
      setBusy(null)
    }
  }

  return (
    <div className="ws-tab">
      <Callout tone="info" title="What these settings do on this server">
        The open-source server delivers run alerts only through a notifier extension. Without one, these webhooks are used by the Slack test message alone.
      </Callout>

      <section className="ws-card">
        <h3>
          Slack <Badge tone={s.webhook_configured ? 'success' : 'neutral'}>{s.webhook_configured ? `Webhook set ${s.webhook_masked}` : 'No webhook'}</Badge>
        </h3>
        <fieldset className="ws-fieldset" disabled={!canEdit}>
          <Field label="Webhook URL" hint={s.webhook_configured ? 'Leave empty to keep the current webhook.' : undefined}>
            <Input type="password" autoComplete="off" value={slackUrl} placeholder="https://hooks.slack.com/services/..." onChange={(e) => setSlackUrl(e.target.value)} />
          </Field>
          <div className="ws-row-2">
            <Field label="Channel">
              <Input value={channel} placeholder="#data-alerts" onChange={(e) => setChannel(e.target.value)} />
            </Field>
            <Field label="Bot name">
              <Input value={botName} onChange={(e) => setBotName(e.target.value)} />
            </Field>
          </div>
          <div className="ws-button-row">
            <Button variant="primary" loading={busy === 'slack'} onClick={() => void save('slack')}>
              Save Slack settings
            </Button>
            {s.webhook_configured && (
              <Button icon={<Send size={14} aria-hidden="true" />} loading={busy === 'test'} onClick={() => void test()}>
                Send a test message
              </Button>
            )}
          </div>
        </fieldset>
        {testResult && <Callout tone={testResult.ok ? 'success' : 'danger'}>{testResult.text}</Callout>}
      </section>

      <section className="ws-card">
        <h3>
          Microsoft Teams <Badge tone={s.teams_configured ? 'success' : 'neutral'}>{s.teams_configured ? `Webhook set ${s.teams_webhook_masked}` : 'No webhook'}</Badge>
        </h3>
        <fieldset className="ws-fieldset" disabled={!canEdit}>
          <Field label="Incoming webhook URL" hint="Create it in Teams under the channel's Connectors, Incoming Webhook. There is no test message for Teams.">
            <Input type="password" autoComplete="off" value={teamsUrl} placeholder="https://your-org.webhook.office.com/..." onChange={(e) => setTeamsUrl(e.target.value)} />
          </Field>
          <div className="ws-button-row">
            <Button variant="primary" loading={busy === 'teams'} disabled={!teamsUrl} onClick={() => void save('teams')}>
              Save Teams webhook
            </Button>
          </div>
        </fieldset>
      </section>

      {canEdit && (s.webhook_configured || s.teams_configured) && (
        <section className="ws-card">
          <h3>Remove webhooks</h3>
          <p className="ws-muted">The server removes the Slack and Teams settings together; it cannot remove only one.</p>
          <div>
            <Button variant="danger" onClick={() => setClearing(true)}>
              Remove Slack and Teams settings
            </Button>
          </div>
        </section>
      )}

      {clearing && (
        <ConfirmDialog
          title="Remove both webhooks?"
          tone="danger"
          confirmLabel="Remove"
          onCancel={() => setClearing(false)}
          onConfirm={async () => {
            await notificationApi.clearAll()
            toast.success('Slack and Teams settings removed')
            setClearing(false)
            await settings.refetch()
          }}
        >
          <p>The Slack webhook, channel, bot name and the Teams webhook are all cleared.</p>
        </ConfirmDialog>
      )}
    </div>
  )
}
