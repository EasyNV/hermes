import type { InboxChannel } from '@/api/types'

interface Props {
  channel: InboxChannel
}

// Renders a small "MBS" pill next to a contact name when the conversation
// is on the MBS channel. WA is the default and gets no badge to avoid
// visual noise.
export function ChannelBadge({ channel }: Props) {
  if (channel !== 'INBOX_CHANNEL_MBS') return null
  return (
    <span
      className="ml-1.5 inline-flex items-center rounded bg-blue-100 px-1 text-[10px] font-medium leading-4 text-blue-700"
      title="Meta Business Suite conversation"
    >
      MBS
    </span>
  )
}
