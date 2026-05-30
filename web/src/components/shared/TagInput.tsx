import { useState } from 'react'
import { X } from 'lucide-react'
import { Badge } from '@/components/ui/badge'

// TagInput — text input with chip-style tags.
//
// Behaviour:
//   - Enter on a non-empty trimmed input adds it (dedup against current value)
//   - Backspace on empty input removes the last tag (no confirm)
//   - Click the × on a chip removes that tag
//
// Originally lived inline in pages/Contacts.tsx; extracted to shared in
// Stage-F-followup chunk 7 so the campaign-create wizard's contact picker
// can reuse the same UI.
export interface TagInputProps {
  value: string[]
  onChange: (tags: string[]) => void
  placeholder?: string
}

export function TagInput({ value, onChange, placeholder = 'Type and press Enter' }: TagInputProps) {
  const [input, setInput] = useState('')

  function handleKeyDown(e: React.KeyboardEvent<HTMLInputElement>) {
    if (e.key === 'Enter' && input.trim()) {
      e.preventDefault()
      const tag = input.trim()
      if (!value.includes(tag)) {
        onChange([...value, tag])
      }
      setInput('')
    }
    if (e.key === 'Backspace' && !input && value.length > 0) {
      onChange(value.slice(0, -1))
    }
  }

  function removeTag(tag: string) {
    onChange(value.filter((t) => t !== tag))
  }

  return (
    <div className="flex flex-wrap items-center gap-1.5 rounded-md border border-input bg-background px-3 py-2 text-sm ring-offset-background focus-within:ring-2 focus-within:ring-ring focus-within:ring-offset-2">
      {value.map((tag) => (
        <Badge key={tag} variant="secondary" className="gap-1">
          {tag}
          <button type="button" onClick={() => removeTag(tag)} className="ml-0.5 hover:text-destructive">
            <X className="h-3 w-3" />
          </button>
        </Badge>
      ))}
      <input
        className="flex-1 bg-transparent outline-none placeholder:text-muted-foreground min-w-[80px]"
        value={input}
        onChange={(e) => setInput(e.target.value)}
        onKeyDown={handleKeyDown}
        placeholder={value.length === 0 ? placeholder : ''}
      />
    </div>
  )
}
