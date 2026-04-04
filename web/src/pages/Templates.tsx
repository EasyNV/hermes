import { useState, useCallback } from 'react'
import { useQuery, useMutation, useQueryClient } from '@tanstack/react-query'
import { Search, Plus, MoreHorizontal, Pencil, Trash2, Image } from 'lucide-react'
import { useAuthStore } from '@/stores/auth'
import { useDebounce } from '@/hooks/useDebounce'
import { listTemplates, createTemplate, updateTemplate, deleteTemplate } from '@/api/templates'
import type { Template } from '@/api/types'
import { truncate } from '@/lib/utils'
import { Pagination } from '@/components/shared/Pagination'
import { ConfirmDialog } from '@/components/shared/ConfirmDialog'
import { Button } from '@/components/ui/button'
import { Input } from '@/components/ui/input'
import { Label } from '@/components/ui/label'
import { Textarea } from '@/components/ui/textarea'
import { Badge } from '@/components/ui/badge'
import {
  Table, TableHeader, TableBody, TableHead, TableRow, TableCell,
} from '@/components/ui/table'
import {
  Dialog, DialogContent, DialogHeader, DialogTitle, DialogDescription, DialogFooter,
} from '@/components/ui/dialog'
import {
  DropdownMenu, DropdownMenuTrigger, DropdownMenuContent, DropdownMenuItem,
} from '@/components/ui/dropdown-menu'

// ── Template Form Dialog ────────────────────────────────────
interface TemplateFormDialogProps {
  open: boolean
  onClose: () => void
  workspaceId: string
  template?: Template | null
}

function TemplateFormDialog({ open, onClose, workspaceId, template }: TemplateFormDialogProps) {
  const queryClient = useQueryClient()
  const isEditing = !!template

  const [name, setName] = useState(template?.name ?? '')
  const [body, setBody] = useState(template?.body ?? '')
  const [mediaUrl, setMediaUrl] = useState(template?.mediaUrl ?? '')

  const createMutation = useMutation({
    mutationFn: () =>
      createTemplate({
        workspaceId,
        name,
        body,
        mediaUrl: mediaUrl || undefined,
      }),
    onSuccess: () => {
      queryClient.invalidateQueries({ queryKey: ['templates'] })
      handleClose()
    },
  })

  const updateMutation = useMutation({
    mutationFn: () => {
      if (!template) throw new Error('No template to update')
      return updateTemplate(template.id, {
        name,
        body,
        mediaUrl: mediaUrl || undefined,
      })
    },
    onSuccess: () => {
      queryClient.invalidateQueries({ queryKey: ['templates'] })
      handleClose()
    },
  })

  function handleClose() {
    setName('')
    setBody('')
    setMediaUrl('')
    onClose()
  }

  function handleSubmit(e: React.FormEvent) {
    e.preventDefault()
    if (isEditing) {
      updateMutation.mutate()
    } else {
      createMutation.mutate()
    }
  }

  const isPending = createMutation.isPending || updateMutation.isPending
  const error = createMutation.error || updateMutation.error

  // Extract detected variables from body
  const detectedVariables = extractVariables(body)

  return (
    <Dialog open={open} onOpenChange={(v) => !v && handleClose()}>
      <DialogContent className="max-w-lg">
        <DialogHeader>
          <DialogTitle>{isEditing ? 'Edit Template' : 'Create Template'}</DialogTitle>
          <DialogDescription>
            {isEditing
              ? 'Update your message template below.'
              : 'Create a new message template for campaigns.'}
          </DialogDescription>
        </DialogHeader>
        <form onSubmit={handleSubmit} className="space-y-4">
          <div>
            <Label htmlFor="template-name">Name</Label>
            <Input
              id="template-name"
              value={name}
              onChange={(e) => setName(e.target.value)}
              placeholder="e.g. Welcome Message"
              required
              className="mt-1"
            />
          </div>
          <div>
            <Label htmlFor="template-body">Body</Label>
            <Textarea
              id="template-body"
              value={body}
              onChange={(e) => setBody(e.target.value)}
              placeholder="Hello {{name}}, welcome to our service!"
              required
              rows={6}
              className="mt-1 font-mono text-sm"
            />
            <p className="mt-1.5 text-xs text-muted-foreground">
              Use <code className="rounded bg-muted px-1 py-0.5">{'{{variable}}'}</code> for
              placeholders and <code className="rounded bg-muted px-1 py-0.5">{'{option1|option2}'}</code> for
              spintax (randomly picks one option per send).
            </p>
            {detectedVariables.length > 0 && (
              <div className="mt-2 flex flex-wrap gap-1">
                <span className="text-xs text-muted-foreground mr-1">Variables:</span>
                {detectedVariables.map((v) => (
                  <Badge key={v} variant="info">{v}</Badge>
                ))}
              </div>
            )}
          </div>
          <div>
            <Label htmlFor="template-media-url">Media URL (optional)</Label>
            <Input
              id="template-media-url"
              value={mediaUrl}
              onChange={(e) => setMediaUrl(e.target.value)}
              placeholder="https://example.com/image.jpg"
              className="mt-1"
            />
          </div>
          {error && (
            <p className="text-sm text-destructive">
              {error instanceof Error ? error.message : 'An error occurred'}
            </p>
          )}
          <DialogFooter>
            <Button type="button" variant="outline" onClick={handleClose}>Cancel</Button>
            <Button type="submit" disabled={isPending || !name.trim() || !body.trim()}>
              {isPending ? 'Saving...' : isEditing ? 'Save Changes' : 'Create'}
            </Button>
          </DialogFooter>
        </form>
      </DialogContent>
    </Dialog>
  )
}

// ── Helpers ─────────────────────────────────────────────────
function extractVariables(body: string): string[] {
  const matches = body.match(/\{\{(\w+)\}\}/g)
  if (!matches) return []
  const unique = new Set(matches.map((m) => m.replace(/\{\{|\}\}/g, '')))
  return Array.from(unique)
}

// ── Main Page ───────────────────────────────────────────────
export default function Templates() {
  const workspace = useAuthStore((s) => s.workspace)
  const workspaceId = workspace?.id ?? ''
  const queryClient = useQueryClient()

  const [search, setSearch] = useState('')
  const debouncedSearch = useDebounce(search, 300)
  const [page, setPage] = useState(1)

  const [formOpen, setFormOpen] = useState(false)
  const [editingTemplate, setEditingTemplate] = useState<Template | null>(null)
  const [deleteTarget, setDeleteTarget] = useState<Template | null>(null)

  const { data, isLoading } = useQuery({
    queryKey: ['templates', workspaceId, debouncedSearch, page],
    queryFn: () =>
      listTemplates({
        workspaceId,
        search: debouncedSearch || undefined,
        page,
        pageSize: 20,
      }),
    enabled: !!workspaceId,
  })

  const deleteMutation = useMutation({
    mutationFn: (id: string) => deleteTemplate(id),
    onSuccess: () => {
      queryClient.invalidateQueries({ queryKey: ['templates'] })
      setDeleteTarget(null)
    },
  })

  const handleEdit = useCallback((template: Template) => {
    setEditingTemplate(template)
    setFormOpen(true)
  }, [])

  const handleCloseForm = useCallback(() => {
    setFormOpen(false)
    setEditingTemplate(null)
  }, [])

  return (
    <div className="space-y-6">
      <div className="flex items-center justify-between">
        <h1 className="text-2xl font-bold tracking-tight">Templates</h1>
        <Button onClick={() => { setEditingTemplate(null); setFormOpen(true) }}>
          <Plus className="mr-2 h-4 w-4" />
          Create Template
        </Button>
      </div>

      {/* Search */}
      <div className="relative max-w-sm">
        <Search className="absolute left-3 top-1/2 h-4 w-4 -translate-y-1/2 text-muted-foreground" />
        <Input
          placeholder="Search templates..."
          value={search}
          onChange={(e) => { setSearch(e.target.value); setPage(1) }}
          className="pl-9"
        />
      </div>

      {/* Table */}
      <div className="rounded-md border">
        <Table>
          <TableHeader>
            <TableRow>
              <TableHead>Name</TableHead>
              <TableHead>Body</TableHead>
              <TableHead>Media</TableHead>
              <TableHead>Variables</TableHead>
              <TableHead>Created</TableHead>
              <TableHead className="w-[50px]" />
            </TableRow>
          </TableHeader>
          <TableBody>
            {isLoading && (
              <TableRow>
                <TableCell colSpan={6} className="text-center py-8 text-muted-foreground">
                  Loading templates...
                </TableCell>
              </TableRow>
            )}
            {!isLoading && data?.templates.length === 0 && (
              <TableRow>
                <TableCell colSpan={6} className="text-center py-8 text-muted-foreground">
                  No templates found.
                </TableCell>
              </TableRow>
            )}
            {data?.templates.map((template) => (
              <TableRow key={template.id}>
                <TableCell className="font-medium">{template.name}</TableCell>
                <TableCell className="max-w-xs">
                  <span className="text-sm text-muted-foreground">
                    {truncate(template.body, 80)}
                  </span>
                </TableCell>
                <TableCell>
                  {template.mediaUrl ? (
                    <a
                      href={template.mediaUrl}
                      target="_blank"
                      rel="noopener noreferrer"
                      className="inline-flex items-center gap-1 text-sm text-blue-600 hover:underline"
                    >
                      <Image className="h-4 w-4" />
                      Media
                    </a>
                  ) : (
                    <span className="text-muted-foreground">-</span>
                  )}
                </TableCell>
                <TableCell>
                  <div className="flex flex-wrap gap-1">
                    {template.variables.length > 0
                      ? template.variables.map((v) => (
                          <Badge key={v} variant="info">{v}</Badge>
                        ))
                      : <span className="text-muted-foreground">-</span>}
                  </div>
                </TableCell>
                <TableCell className="text-muted-foreground">
                  {new Date(template.createdAt).toLocaleDateString()}
                </TableCell>
                <TableCell>
                  <DropdownMenu>
                    <DropdownMenuTrigger asChild>
                      <Button variant="ghost" size="sm" className="h-8 w-8 p-0">
                        <MoreHorizontal className="h-4 w-4" />
                      </Button>
                    </DropdownMenuTrigger>
                    <DropdownMenuContent align="end">
                      <DropdownMenuItem onClick={() => handleEdit(template)}>
                        <Pencil className="mr-2 h-4 w-4" /> Edit
                      </DropdownMenuItem>
                      <DropdownMenuItem
                        className="text-destructive focus:text-destructive"
                        onClick={() => setDeleteTarget(template)}
                      >
                        <Trash2 className="mr-2 h-4 w-4" /> Delete
                      </DropdownMenuItem>
                    </DropdownMenuContent>
                  </DropdownMenu>
                </TableCell>
              </TableRow>
            ))}
          </TableBody>
        </Table>
      </div>

      {data?.pagination && (
        <Pagination pagination={data.pagination} onPageChange={setPage} />
      )}

      {/* Dialogs */}
      <TemplateFormDialog
        key={editingTemplate?.id ?? 'new'}
        open={formOpen}
        onClose={handleCloseForm}
        workspaceId={workspaceId}
        template={editingTemplate}
      />

      <ConfirmDialog
        open={!!deleteTarget}
        onClose={() => setDeleteTarget(null)}
        onConfirm={() => deleteTarget && deleteMutation.mutate(deleteTarget.id)}
        title="Delete Template"
        description={`Are you sure you want to delete "${deleteTarget?.name}"? Campaigns using this template will not be affected, but you won't be able to use it for new campaigns.`}
        confirmLabel="Delete"
        destructive
        loading={deleteMutation.isPending}
      />
    </div>
  )
}
