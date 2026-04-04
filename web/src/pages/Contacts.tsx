import { useState, useCallback, useRef } from 'react'
import { useQuery, useMutation, useQueryClient } from '@tanstack/react-query'
import { Search, Plus, Upload, MoreHorizontal, Pencil, Trash2, X } from 'lucide-react'
import { useAuthStore } from '@/stores/auth'
import { useDebounce } from '@/hooks/useDebounce'
import { listContacts, createContact, updateContact, deleteContact, importContacts } from '@/api/contacts'
import type { Contact } from '@/api/types'
import { formatPhone, truncate } from '@/lib/utils'
import { Pagination } from '@/components/shared/Pagination'
import { ConfirmDialog } from '@/components/shared/ConfirmDialog'
import { Button } from '@/components/ui/button'
import { Input } from '@/components/ui/input'
import { Label } from '@/components/ui/label'
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
import {
  Select, SelectTrigger, SelectValue, SelectContent, SelectItem,
} from '@/components/ui/select'

// ── Tag Input ───────────────────────────────────────────────
interface TagInputProps {
  value: string[]
  onChange: (tags: string[]) => void
  placeholder?: string
}

function TagInput({ value, onChange, placeholder = 'Type and press Enter' }: TagInputProps) {
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

// ── CSV Column Mapping ──────────────────────────────────────
const MAPPABLE_FIELDS = ['phone', 'name', 'tags'] as const
type MappableField = typeof MAPPABLE_FIELDS[number]

interface ColumnMapping {
  [csvHeader: string]: MappableField | ''
}

// ── Import Dialog ───────────────────────────────────────────
interface ImportDialogProps {
  open: boolean
  onClose: () => void
  tenantId: string
}

function ImportDialog({ open, onClose, tenantId }: ImportDialogProps) {
  const queryClient = useQueryClient()
  const [csvHeaders, setCsvHeaders] = useState<string[]>([])
  const [columnMapping, setColumnMapping] = useState<ColumnMapping>({})
  const [defaultTags, setDefaultTags] = useState<string[]>([])
  const [csvBase64, setCsvBase64] = useState('')
  const [filename, setFilename] = useState('')
  const fileInputRef = useRef<HTMLInputElement>(null)

  const importMutation = useMutation({
    mutationFn: importContacts,
    onSuccess: () => {
      queryClient.invalidateQueries({ queryKey: ['contacts'] })
      resetAndClose()
    },
  })

  function resetAndClose() {
    setCsvHeaders([])
    setColumnMapping({})
    setDefaultTags([])
    setCsvBase64('')
    setFilename('')
    onClose()
  }

  function handleFileChange(e: React.ChangeEvent<HTMLInputElement>) {
    const file = e.target.files?.[0]
    if (!file) return

    setFilename(file.name)
    const reader = new FileReader()
    reader.onload = () => {
      const text = reader.result as string
      const base64 = btoa(unescape(encodeURIComponent(text)))
      setCsvBase64(base64)

      // Parse first line for headers
      const firstLine = text.split('\n')[0]
      if (firstLine) {
        const headers = firstLine.split(',').map((h) => h.trim().replace(/^"|"$/g, ''))
        setCsvHeaders(headers)
        // Auto-map obvious columns
        const autoMapping: ColumnMapping = {}
        headers.forEach((header) => {
          const lower = header.toLowerCase()
          if (lower.includes('phone') || lower.includes('number') || lower.includes('whatsapp')) {
            autoMapping[header] = 'phone'
          } else if (lower.includes('name')) {
            autoMapping[header] = 'name'
          } else if (lower.includes('tag')) {
            autoMapping[header] = 'tags'
          } else {
            autoMapping[header] = ''
          }
        })
        setColumnMapping(autoMapping)
      }
    }
    reader.readAsText(file)
  }

  function handleMappingChange(csvHeader: string, field: string) {
    setColumnMapping((prev) => ({ ...prev, [csvHeader]: field as MappableField | '' }))
  }

  function handleImport() {
    const mapping: Record<string, string> = {}
    for (const [csvCol, field] of Object.entries(columnMapping)) {
      if (field) {
        mapping[csvCol] = field
      }
    }
    importMutation.mutate({
      tenantId,
      csvData: csvBase64,
      filename,
      columnMapping: mapping,
      defaultTags: defaultTags.length > 0 ? defaultTags : undefined,
    })
  }

  const hasPhoneMapping = Object.values(columnMapping).includes('phone')

  return (
    <Dialog open={open} onOpenChange={(v) => !v && resetAndClose()}>
      <DialogContent className="max-w-lg">
        <DialogHeader>
          <DialogTitle>Import Contacts</DialogTitle>
          <DialogDescription>Upload a CSV file and map columns to contact fields.</DialogDescription>
        </DialogHeader>

        <div className="space-y-4">
          <div>
            <Label>CSV File</Label>
            <Input
              ref={fileInputRef}
              type="file"
              accept=".csv"
              onChange={handleFileChange}
              className="mt-1"
            />
          </div>

          {csvHeaders.length > 0 && (
            <div className="space-y-3">
              <Label>Column Mapping</Label>
              {csvHeaders.map((header) => (
                <div key={header} className="flex items-center gap-3">
                  <span className="w-1/3 text-sm font-medium truncate" title={header}>
                    {header}
                  </span>
                  <Select
                    value={columnMapping[header] || 'skip'}
                    onValueChange={(v) => handleMappingChange(header, v === 'skip' ? '' : v)}
                  >
                    <SelectTrigger className="w-2/3">
                      <SelectValue />
                    </SelectTrigger>
                    <SelectContent>
                      <SelectItem value="skip">-- Skip --</SelectItem>
                      {MAPPABLE_FIELDS.map((field) => (
                        <SelectItem key={field} value={field}>
                          {field.charAt(0).toUpperCase() + field.slice(1)}
                        </SelectItem>
                      ))}
                    </SelectContent>
                  </Select>
                </div>
              ))}
            </div>
          )}

          <div>
            <Label>Default Tags (applied to all imported contacts)</Label>
            <div className="mt-1">
              <TagInput value={defaultTags} onChange={setDefaultTags} placeholder="Add default tags" />
            </div>
          </div>

          {importMutation.isError && (
            <p className="text-sm text-destructive">
              Import failed. Please check your CSV and try again.
            </p>
          )}

          {importMutation.isSuccess && importMutation.data && (
            <div className="text-sm space-y-1">
              <p className="text-green-700">Imported: {importMutation.data.importedCount}</p>
              {importMutation.data.skippedCount > 0 && (
                <p className="text-yellow-700">Skipped (duplicates): {importMutation.data.skippedCount}</p>
              )}
              {importMutation.data.failedCount > 0 && (
                <p className="text-red-700">Failed: {importMutation.data.failedCount}</p>
              )}
            </div>
          )}
        </div>

        <DialogFooter>
          <Button variant="outline" onClick={resetAndClose}>Cancel</Button>
          <Button
            onClick={handleImport}
            disabled={!csvBase64 || !hasPhoneMapping || importMutation.isPending}
          >
            {importMutation.isPending ? 'Importing...' : 'Import'}
          </Button>
        </DialogFooter>
      </DialogContent>
    </Dialog>
  )
}

// ── Create / Edit Dialog ────────────────────────────────────
interface ContactFormDialogProps {
  open: boolean
  onClose: () => void
  tenantId: string
  contact?: Contact | null
}

function ContactFormDialog({ open, onClose, tenantId, contact }: ContactFormDialogProps) {
  const queryClient = useQueryClient()
  const [phone, setPhone] = useState(contact?.phone ?? '')
  const [name, setName] = useState(contact?.name ?? '')
  const [tags, setTags] = useState<string[]>(contact?.tags ?? [])

  const isEditing = !!contact

  const createMutation = useMutation({
    mutationFn: () => createContact({ tenantId, phone, name: name || undefined, tags: tags.length > 0 ? tags : undefined }),
    onSuccess: () => {
      queryClient.invalidateQueries({ queryKey: ['contacts'] })
      handleClose()
    },
  })

  const updateMutation = useMutation({
    mutationFn: () => {
      if (!contact) throw new Error('No contact to update')
      return updateContact(contact.id, { phone, name, tags })
    },
    onSuccess: () => {
      queryClient.invalidateQueries({ queryKey: ['contacts'] })
      handleClose()
    },
  })

  function handleClose() {
    setPhone('')
    setName('')
    setTags([])
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

  // Sync form when contact changes (for edit)
  // Using key on Dialog is a simpler pattern, but we handle it here for explicitness
  useState(() => {
    if (contact) {
      setPhone(contact.phone)
      setName(contact.name)
      setTags(contact.tags)
    }
  })

  return (
    <Dialog open={open} onOpenChange={(v) => !v && handleClose()}>
      <DialogContent>
        <DialogHeader>
          <DialogTitle>{isEditing ? 'Edit Contact' : 'Create Contact'}</DialogTitle>
          <DialogDescription>
            {isEditing ? 'Update the contact details below.' : 'Add a new contact to your tenant.'}
          </DialogDescription>
        </DialogHeader>
        <form onSubmit={handleSubmit} className="space-y-4">
          <div>
            <Label htmlFor="contact-phone">Phone</Label>
            <Input
              id="contact-phone"
              value={phone}
              onChange={(e) => setPhone(e.target.value)}
              placeholder="+628123456789"
              required
              className="mt-1"
            />
          </div>
          <div>
            <Label htmlFor="contact-name">Name</Label>
            <Input
              id="contact-name"
              value={name}
              onChange={(e) => setName(e.target.value)}
              placeholder="Contact name"
              className="mt-1"
            />
          </div>
          <div>
            <Label>Tags</Label>
            <div className="mt-1">
              <TagInput value={tags} onChange={setTags} placeholder="Add tags" />
            </div>
          </div>
          {error && (
            <p className="text-sm text-destructive">
              {error instanceof Error ? error.message : 'An error occurred'}
            </p>
          )}
          <DialogFooter>
            <Button type="button" variant="outline" onClick={handleClose}>Cancel</Button>
            <Button type="submit" disabled={isPending || !phone.trim()}>
              {isPending ? 'Saving...' : isEditing ? 'Save Changes' : 'Create'}
            </Button>
          </DialogFooter>
        </form>
      </DialogContent>
    </Dialog>
  )
}

// ── Main Page ───────────────────────────────────────────────
export default function Contacts() {
  const tenantId = useAuthStore((s) => s.tenant?.id ?? '')
  const queryClient = useQueryClient()

  const [search, setSearch] = useState('')
  const debouncedSearch = useDebounce(search, 300)
  const [page, setPage] = useState(1)
  const [filterTags, setFilterTags] = useState<string[]>([])

  const [importOpen, setImportOpen] = useState(false)
  const [formOpen, setFormOpen] = useState(false)
  const [editingContact, setEditingContact] = useState<Contact | null>(null)
  const [deleteTarget, setDeleteTarget] = useState<Contact | null>(null)

  const { data, isLoading } = useQuery({
    queryKey: ['contacts', tenantId, debouncedSearch, page, filterTags],
    queryFn: () =>
      listContacts({
        tenantId,
        search: debouncedSearch || undefined,
        tags: filterTags.length > 0 ? filterTags : undefined,
        page,
        pageSize: 20,
      }),
    enabled: !!tenantId,
  })

  const deleteMutation = useMutation({
    mutationFn: (id: string) => deleteContact(id),
    onSuccess: () => {
      queryClient.invalidateQueries({ queryKey: ['contacts'] })
      setDeleteTarget(null)
    },
  })

  const handleEdit = useCallback((contact: Contact) => {
    setEditingContact(contact)
    setFormOpen(true)
  }, [])

  const handleCloseForm = useCallback(() => {
    setFormOpen(false)
    setEditingContact(null)
  }, [])

  return (
    <div className="space-y-6">
      <div className="flex items-center justify-between">
        <h1 className="text-2xl font-bold tracking-tight">Contacts</h1>
        <div className="flex items-center gap-2">
          <Button variant="outline" onClick={() => setImportOpen(true)}>
            <Upload className="mr-2 h-4 w-4" />
            Import CSV
          </Button>
          <Button onClick={() => { setEditingContact(null); setFormOpen(true) }}>
            <Plus className="mr-2 h-4 w-4" />
            Add Contact
          </Button>
        </div>
      </div>

      {/* Filters */}
      <div className="flex flex-col gap-3 sm:flex-row sm:items-end">
        <div className="relative flex-1">
          <Search className="absolute left-3 top-1/2 h-4 w-4 -translate-y-1/2 text-muted-foreground" />
          <Input
            placeholder="Search by name or phone..."
            value={search}
            onChange={(e) => { setSearch(e.target.value); setPage(1) }}
            className="pl-9"
          />
        </div>
        <div className="w-full sm:w-72">
          <TagInput
            value={filterTags}
            onChange={(tags) => { setFilterTags(tags); setPage(1) }}
            placeholder="Filter by tags..."
          />
        </div>
      </div>

      {/* Table */}
      <div className="rounded-md border">
        <Table>
          <TableHeader>
            <TableRow>
              <TableHead>Phone</TableHead>
              <TableHead>Name</TableHead>
              <TableHead>Tags</TableHead>
              <TableHead>Ban Status</TableHead>
              <TableHead>Created</TableHead>
              <TableHead className="w-[50px]" />
            </TableRow>
          </TableHeader>
          <TableBody>
            {isLoading && (
              <TableRow>
                <TableCell colSpan={6} className="text-center py-8 text-muted-foreground">
                  Loading contacts...
                </TableCell>
              </TableRow>
            )}
            {!isLoading && data?.contacts.length === 0 && (
              <TableRow>
                <TableCell colSpan={6} className="text-center py-8 text-muted-foreground">
                  No contacts found.
                </TableCell>
              </TableRow>
            )}
            {data?.contacts.map((contact) => (
              <TableRow key={contact.id}>
                <TableCell className="font-mono text-sm">
                  {formatPhone(contact.phone)}
                </TableCell>
                <TableCell>{contact.name || '-'}</TableCell>
                <TableCell>
                  <div className="flex flex-wrap gap-1">
                    {contact.tags.length > 0
                      ? contact.tags.map((tag) => (
                          <Badge key={tag} variant="secondary">{tag}</Badge>
                        ))
                      : <span className="text-muted-foreground">-</span>}
                  </div>
                </TableCell>
                <TableCell>
                  {contact.isBanned ? (
                    <Badge variant="destructive">Banned</Badge>
                  ) : (
                    <Badge variant="success">Clean</Badge>
                  )}
                </TableCell>
                <TableCell className="text-muted-foreground">
                  {new Date(contact.createdAt).toLocaleDateString()}
                </TableCell>
                <TableCell>
                  <DropdownMenu>
                    <DropdownMenuTrigger asChild>
                      <Button variant="ghost" size="sm" className="h-8 w-8 p-0">
                        <MoreHorizontal className="h-4 w-4" />
                      </Button>
                    </DropdownMenuTrigger>
                    <DropdownMenuContent align="end">
                      <DropdownMenuItem onClick={() => handleEdit(contact)}>
                        <Pencil className="mr-2 h-4 w-4" /> Edit
                      </DropdownMenuItem>
                      <DropdownMenuItem
                        className="text-destructive focus:text-destructive"
                        onClick={() => setDeleteTarget(contact)}
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
      <ImportDialog open={importOpen} onClose={() => setImportOpen(false)} tenantId={tenantId} />

      <ContactFormDialog
        key={editingContact?.id ?? 'new'}
        open={formOpen}
        onClose={handleCloseForm}
        tenantId={tenantId}
        contact={editingContact}
      />

      <ConfirmDialog
        open={!!deleteTarget}
        onClose={() => setDeleteTarget(null)}
        onConfirm={() => deleteTarget && deleteMutation.mutate(deleteTarget.id)}
        title="Delete Contact"
        description={`Are you sure you want to delete ${deleteTarget?.name || deleteTarget?.phone || 'this contact'}? This action cannot be undone.`}
        confirmLabel="Delete"
        destructive
        loading={deleteMutation.isPending}
      />
    </div>
  )
}
