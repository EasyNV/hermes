import { Badge } from '@/components/ui/badge'
import { cn } from '@/lib/utils'
import type { StatusVariant } from '@/lib/constants'

interface StatusBadgeProps {
  label: string
  variant: StatusVariant
  dot?: string
  className?: string
  pulse?: boolean
}

export function StatusBadge({ label, variant, dot, className, pulse }: StatusBadgeProps) {
  return (
    <Badge variant={variant} className={cn('gap-1.5', className)}>
      {dot && (
        <span className={cn('inline-block h-2 w-2 rounded-full', dot, pulse && 'animate-pulse')} />
      )}
      {label}
    </Badge>
  )
}
