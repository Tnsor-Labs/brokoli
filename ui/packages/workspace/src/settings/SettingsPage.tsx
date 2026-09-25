import { useSearchParams } from 'react-router-dom'
import { Page, PageHeader, Tabs } from '@brokoli/ui'
import { AccountTab } from './AccountTab'
import { ApiTab } from './ApiTab'
import { GeneralTab } from './GeneralTab'
import { NotificationsTab } from './NotificationsTab'
import { UsersTab } from './UsersTab'
import '../workspace.css'

type Tab = 'general' | 'users' | 'account' | 'notifications' | 'api'
const TABS: { id: Tab; label: string }[] = [
  { id: 'general', label: 'General' },
  { id: 'users', label: 'Users' },
  { id: 'account', label: 'Your account' },
  { id: 'notifications', label: 'Notifications' },
  { id: 'api', label: 'API and CLI' },
]

/** Settings, with the tab kept in the URL (?tab=) so it survives reloads and can be linked. */
export function SettingsPage({ initialTab }: { initialTab?: Tab }) {
  const [params, setParams] = useSearchParams()
  const requested = params.get('tab') as Tab | null
  const tab = TABS.some((t) => t.id === requested) ? requested! : (initialTab ?? 'general')
  return (
    <Page>
      <PageHeader eyebrow="Server" title="Settings" description="Server status and maintenance, people, notifications and the API." />
      <Tabs label="Settings sections" items={TABS} value={tab} onChange={(id) => setParams({ tab: id }, { replace: true })} />
      <div className="ws-tab-body">
        {tab === 'general' && <GeneralTab />}
        {tab === 'users' && <UsersTab />}
        {tab === 'account' && <AccountTab />}
        {tab === 'notifications' && <NotificationsTab />}
        {tab === 'api' && <ApiTab />}
      </div>
    </Page>
  )
}
