import { InboxChannel } from '@/api/types'

interface Props {
  value: InboxChannel | undefined
  onChange: (v: InboxChannel | undefined) => void
}

// Segmented control for the inbox conversation list. `undefined`
// represents "All" (no channel filter). E3 chunk 5.
export function ChannelFilter({ value, onChange }: Props) {
  const options: Array<{ label: string; val: InboxChannel | undefined }> = [
    { label: 'All', val: undefined },
    { label: 'WA', val: InboxChannel.WA },
    { label: 'MBS', val: InboxChannel.MBS },
  ]
  return (
    <div className="inline-flex rounded border border-gray-200 bg-white p-0.5 text-xs">
      {options.map((opt) => {
        const active = value === opt.val
        return (
          <button
            key={opt.label}
            type="button"
            onClick={() => onChange(opt.val)}
            className={
              'rounded px-2.5 py-1 transition-colors ' +
              (active
                ? 'bg-blue-600 text-white'
                : 'text-gray-600 hover:bg-gray-100')
            }
          >
            {opt.label}
          </button>
        )
      })}
    </div>
  )
}
