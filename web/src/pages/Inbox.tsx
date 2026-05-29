import { useState, useEffect, useRef, useCallback } from 'react'
import { useQuery, useMutation, useQueryClient } from '@tanstack/react-query'
import { format } from 'date-fns'
import {
  MessageSquare, Send, MoreVertical, Search, UserPlus, ArrowRightLeft,
  XCircle, Inbox as InboxIcon, Loader2, Trash2, ShieldOff,
} from 'lucide-react'

import { useAuthStore } from '@/stores/auth'
import { useInboxStore } from '@/stores/inbox'
import { useWebSocketStore } from '@/stores/websocket'
import {
  listConversations, getConversation, listMessages, sendMessage,
  claimConversation, transferConversation, closeConversation,
} from '@/api/inbox'
import { listCannedResponses, clearAllConversations, clearAllowlist } from '@/api/inbox'
import type {
  Conversation, Message, CannedResponse, ConversationStatus, InboxChannel,
} from '@/api/types'
import { ConversationStatus as ConvStatus, ContentType, MessageDirection, InboxChannel as InboxChan } from '@/api/types'
import { CONVERSATION_STATUS, MSG_STATUS } from '@/lib/constants'
import { cn, truncate, formatPhone } from '@/lib/utils'

import { ChannelBadge } from '@/components/inbox/ChannelBadge'
import { ChannelFilter } from '@/components/inbox/ChannelFilter'
import { Button } from '@/components/ui/button'
import { Input } from '@/components/ui/input'
import { Textarea } from '@/components/ui/textarea'
import { Badge } from '@/components/ui/badge'
import { ScrollArea } from '@/components/ui/scroll-area'
import { Tabs, TabsList, TabsTrigger, TabsContent } from '@/components/ui/tabs'
import {
  DropdownMenu, DropdownMenuTrigger, DropdownMenuContent,
  DropdownMenuItem, DropdownMenuSeparator,
} from '@/components/ui/dropdown-menu'
import {
  Dialog, DialogContent, DialogHeader, DialogTitle,
  DialogFooter, DialogDescription,
} from '@/components/ui/dialog'
import { StatusBadge } from '@/components/shared/StatusBadge'

// ── Conversation List Item ─────────────────────────────────

interface ConversationItemProps {
  conversation: Conversation
  isActive: boolean
  onClick: () => void
}

function ConversationItem({ conversation, isActive, onClick }: ConversationItemProps) {
  const timeLabel = conversation.lastMessageAt
    ? format(new Date(conversation.lastMessageAt), 'MMM d, HH:mm')
    : ''

  return (
    <button
      onClick={onClick}
      className={cn(
        'flex w-full items-start gap-3 px-4 py-3 text-left transition-colors hover:bg-accent/50',
        isActive && 'bg-accent',
      )}
    >
      <div className="flex h-10 w-10 shrink-0 items-center justify-center rounded-full bg-primary/10 text-primary font-semibold text-sm">
        {(conversation.contactName || conversation.contactPhone || '?').charAt(0).toUpperCase()}
      </div>
      <div className="flex-1 overflow-hidden">
        <div className="flex items-center justify-between">
          <p className="truncate text-sm font-medium">
            {conversation.contactName || formatPhone(conversation.contactPhone)}
            <ChannelBadge channel={conversation.channel} />
          </p>
          <span className="shrink-0 text-xs text-muted-foreground">{timeLabel}</span>
        </div>
        {conversation.contactName && (
          <p className="text-xs text-muted-foreground">{formatPhone(conversation.contactPhone)}</p>
        )}
        <p className="mt-0.5 truncate text-xs text-muted-foreground">
          {truncate(conversation.lastMessagePreview || '', 60)}
        </p>
      </div>
      {conversation.unreadCount > 0 && (
        <Badge variant="default" className="ml-1 shrink-0 px-1.5 py-0.5 text-xs">
          {conversation.unreadCount}
        </Badge>
      )}
    </button>
  )
}

// ── Message Bubble ─────────────────────────────────────────

interface MessageBubbleProps {
  message: Message
}

function MessageBubble({ message }: MessageBubbleProps) {
  const isOutbound = message.direction === MessageDirection.OUTBOUND
  const msgStatus = MSG_STATUS[message.status] ?? MSG_STATUS.MESSAGE_STATUS_UNSPECIFIED
  const timeStr = format(new Date(message.createdAt), 'HH:mm')

  return (
    <div className={cn('flex', isOutbound ? 'justify-end' : 'justify-start')}>
      <div
        className={cn(
          'max-w-[70%] rounded-lg px-3 py-2 text-sm',
          isOutbound
            ? 'bg-primary text-primary-foreground'
            : 'bg-muted text-foreground',
        )}
      >
        {message.body && <p className="whitespace-pre-wrap break-words">{message.body}</p>}
        {message.mediaUrl && (
          <p className="mt-1 text-xs underline">
            <a href={message.mediaUrl} target="_blank" rel="noopener noreferrer">
              View attachment
            </a>
          </p>
        )}
        <div className={cn('mt-1 flex items-center gap-1 text-[10px]', isOutbound ? 'justify-end opacity-80' : 'opacity-60')}>
          <span>{timeStr}</span>
          {isOutbound && <span>{msgStatus.ticks}</span>}
        </div>
      </div>
    </div>
  )
}

// ── Canned Response Dropdown ───────────────────────────────

interface CannedDropdownProps {
  items: CannedResponse[]
  onSelect: (body: string) => void
  onClose: () => void
}

function CannedDropdown({ items, onSelect, onClose }: CannedDropdownProps) {
  if (items.length === 0) return null

  return (
    <div className="absolute bottom-full left-0 mb-1 w-full max-h-48 overflow-y-auto rounded-md border bg-popover shadow-md z-10">
      {items.map((cr) => (
        <button
          key={cr.id}
          className="flex w-full flex-col px-3 py-2 text-left text-sm hover:bg-accent transition-colors"
          onClick={() => { onSelect(cr.body); onClose() }}
        >
          <span className="font-medium text-foreground">{cr.shortcut}</span>
          <span className="text-xs text-muted-foreground">{truncate(cr.body, 80)}</span>
        </button>
      ))}
    </div>
  )
}

// ── Transfer Dialog ────────────────────────────────────────

interface TransferDialogProps {
  open: boolean
  onClose: () => void
  onConfirm: (userId: string) => void
  loading: boolean
}

function TransferDialog({ open, onClose, onConfirm, loading }: TransferDialogProps) {
  const [targetUserId, setTargetUserId] = useState('')

  return (
    <Dialog open={open} onOpenChange={(v) => !v && onClose()}>
      <DialogContent>
        <DialogHeader>
          <DialogTitle>Transfer Conversation</DialogTitle>
          <DialogDescription>Enter the user ID of the agent to transfer this conversation to.</DialogDescription>
        </DialogHeader>
        <Input
          placeholder="Target user ID"
          value={targetUserId}
          onChange={(e) => setTargetUserId(e.target.value)}
        />
        <DialogFooter>
          <Button variant="outline" onClick={onClose} disabled={loading}>Cancel</Button>
          <Button
            onClick={() => onConfirm(targetUserId)}
            disabled={loading || !targetUserId.trim()}
          >
            {loading ? 'Transferring...' : 'Transfer'}
          </Button>
        </DialogFooter>
      </DialogContent>
    </Dialog>
  )
}

// ── Main Inbox Page ────────────────────────────────────────

export default function Inbox() {
  const user = useAuthStore((s) => s.user)
  const workspace = useAuthStore((s) => s.workspace)
  const workspaceId = workspace?.id ?? ''
  const userId = user?.id ?? ''

  const {
    conversations, activeConversationId, messages, typingMap,
    setConversations, setActiveConversation, setMessages, appendMessage,
  } = useInboxStore()

  const { subscribeConversation, unsubscribeConversation } = useWebSocketStore()
  const queryClient = useQueryClient()

  const [activeTab, setActiveTab] = useState<'unassigned' | 'mine'>('unassigned')
  const [searchQuery, setSearchQuery] = useState('')
  const [channelFilter, setChannelFilter] = useState<InboxChannel | undefined>(undefined)
  const [messageText, setMessageText] = useState('')
  const [showCanned, setShowCanned] = useState(false)
  const [showTransfer, setShowTransfer] = useState(false)

  const messagesEndRef = useRef<HTMLDivElement>(null)
  const prevConversationRef = useRef<string | null>(null)

  // ── Queries ──

  const unassignedQuery = useQuery({
    queryKey: ['conversations', 'unassigned', workspaceId, searchQuery, channelFilter],
    queryFn: () => listConversations({
      workspaceId,
      status: ConvStatus.UNASSIGNED as ConversationStatus,
      search: searchQuery || undefined,
      channel: channelFilter,
      pageSize: 50,
    }),
    enabled: !!workspaceId && activeTab === 'unassigned',
  })

  const myQuery = useQuery({
    queryKey: ['conversations', 'mine', workspaceId, userId, searchQuery, channelFilter],
    queryFn: () => listConversations({
      workspaceId,
      assignedTo: userId,
      search: searchQuery || undefined,
      channel: channelFilter,
      pageSize: 50,
    }),
    enabled: !!workspaceId && !!userId && activeTab === 'mine',
  })

  const activeConvQuery = useQuery({
    queryKey: ['conversation', activeConversationId],
    queryFn: () => getConversation(activeConversationId!),
    enabled: !!activeConversationId,
  })

  const messagesQuery = useQuery({
    queryKey: ['messages', activeConversationId],
    queryFn: () => listMessages(activeConversationId!, { pageSize: 100 }),
    enabled: !!activeConversationId,
  })

  const cannedQuery = useQuery({
    queryKey: ['canned-responses', workspaceId],
    queryFn: () => listCannedResponses({ workspaceId, pageSize: 100 }),
    enabled: !!workspaceId,
  })

  // ── Mutations ──

  const sendMutation = useMutation({
    mutationFn: (params: { conversationId: string; body: string }) =>
      sendMessage(params.conversationId, { contentType: ContentType.TEXT, body: params.body }),
    onSuccess: (data) => {
      appendMessage(data.message)
      setMessageText('')
      // Refetch messages to ensure DB state is reflected.
      queryClient.invalidateQueries({ queryKey: ['messages', activeConversationId] })
    },
    onError: (err) => {
      console.error('Send message failed:', err)
    },
  })

  const claimMutation = useMutation({
    mutationFn: (id: string) => claimConversation(id),
    onSuccess: () => {
      queryClient.invalidateQueries({ queryKey: ['conversations'] })
      queryClient.invalidateQueries({ queryKey: ['conversation', activeConversationId] })
    },
  })

  const transferMutation = useMutation({
    mutationFn: (params: { id: string; targetUserId: string }) =>
      transferConversation(params.id, params.targetUserId),
    onSuccess: () => {
      setShowTransfer(false)
      queryClient.invalidateQueries({ queryKey: ['conversations'] })
      queryClient.invalidateQueries({ queryKey: ['conversation', activeConversationId] })
    },
  })

  const closeMutation = useMutation({
    mutationFn: (id: string) => closeConversation(id),
    onSuccess: () => {
      queryClient.invalidateQueries({ queryKey: ['conversations'] })
      queryClient.invalidateQueries({ queryKey: ['conversation', activeConversationId] })
    },
  })

  const clearAllMutation = useMutation({
    mutationFn: () => clearAllConversations(),
    onSuccess: () => {
      setActiveConversation(null)
      queryClient.invalidateQueries({ queryKey: ['conversations'] })
      queryClient.invalidateQueries({ queryKey: ['messages'] })
    },
  })

  const clearAllowlistMutation = useMutation({
    mutationFn: () => clearAllowlist(),
  })

  // ── Sync query data into store ──

  useEffect(() => {
    const data = activeTab === 'unassigned' ? unassignedQuery.data : myQuery.data
    if (data) setConversations(data.conversations)
  }, [activeTab, unassignedQuery.data, myQuery.data, setConversations])

  useEffect(() => {
    if (messagesQuery.data) {
      // API returns newest-first; chat UI needs oldest-first (newest at bottom).
      const sorted = [...messagesQuery.data.messages].reverse()
      setMessages(sorted)
    }
  }, [messagesQuery.data, setMessages])

  // ── WebSocket subscription for active conversation ──

  useEffect(() => {
    const prev = prevConversationRef.current
    if (prev) unsubscribeConversation(prev)
    if (activeConversationId) subscribeConversation(activeConversationId)
    prevConversationRef.current = activeConversationId
    return () => {
      if (activeConversationId) unsubscribeConversation(activeConversationId)
    }
  }, [activeConversationId, subscribeConversation, unsubscribeConversation])

  // ── Auto-scroll to bottom on new messages ──

  useEffect(() => {
    messagesEndRef.current?.scrollIntoView({ behavior: 'smooth' })
  }, [messages])

  // ── Handlers ──

  const handleSelectConversation = useCallback((id: string) => {
    setActiveConversation(id)
    setShowCanned(false)
    setMessageText('')
  }, [setActiveConversation])

  const handleSend = useCallback(() => {
    if (!messageText.trim() || !activeConversationId) return
    sendMutation.mutate({ conversationId: activeConversationId, body: messageText.trim() })
  }, [messageText, activeConversationId, sendMutation])

  const handleKeyDown = useCallback((e: React.KeyboardEvent<HTMLTextAreaElement>) => {
    if (e.key === 'Enter' && !e.shiftKey) {
      e.preventDefault()
      handleSend()
    }
    // Shift+Enter inserts newline (default textarea behavior).
  }, [handleSend])

  const handleInputChange = useCallback((e: React.ChangeEvent<HTMLTextAreaElement>) => {
    const value = e.target.value
    setMessageText(value)
    setShowCanned(value.startsWith('/') && value.length >= 1)
  }, [])

  const handleCannedSelect = useCallback((body: string) => {
    setMessageText(body)
    setShowCanned(false)
  }, [])

  // ── Active conversation details ──

  const activeConv = conversations.find((c) => c.id === activeConversationId)
  const convStatus = activeConv?.status
    ? CONVERSATION_STATUS[activeConv.status]
    : null
  const isTyping = activeConversationId ? typingMap[activeConversationId] ?? false : false

  // ── Canned responses filtered by input ──

  const cannedResponses = cannedQuery.data?.cannedResponses ?? []
  const filteredCanned = showCanned
    ? cannedResponses.filter((cr) =>
        cr.shortcut.toLowerCase().includes(messageText.toLowerCase()),
      )
    : []

  // ── Render ───────────────────────────────────────────────

  return (
    <div className="flex h-full">
      {/* ── Left Panel: Conversation List ── */}
      <div className="flex w-[350px] shrink-0 flex-col border-r">
        <div className="p-4 pb-2">
          <div className="flex items-center justify-between">
            <h1 className="text-lg font-semibold">Inbox</h1>
            <Button
              variant="ghost"
              size="sm"
              className="text-destructive hover:text-destructive"
              onClick={() => {
                if (window.confirm('Clear ALL conversations in this workspace? This cannot be undone.')) {
                  clearAllMutation.mutate()
                }
              }}
              disabled={clearAllMutation.isPending}
            >
              {clearAllMutation.isPending ? <Loader2 className="h-4 w-4 animate-spin" /> : <Trash2 className="h-4 w-4" />}
            </Button>
            <Button
              variant="ghost"
              size="sm"
              title="Clear allowlist"
              className="text-orange-500 hover:text-orange-600"
              onClick={() => {
                if (window.confirm('Clear all allowlisted contacts? New inbound messages will be dropped until contacts are re-added.')) {
                  clearAllowlistMutation.mutate()
                }
              }}
              disabled={clearAllowlistMutation.isPending}
            >
              {clearAllowlistMutation.isPending ? <Loader2 className="h-4 w-4 animate-spin" /> : <ShieldOff className="h-4 w-4" />}
            </Button>
          </div>
          <div className="mt-2 relative">
            <Search className="absolute left-3 top-1/2 h-4 w-4 -translate-y-1/2 text-muted-foreground" />
            <Input
              placeholder="Search conversations..."
              value={searchQuery}
              onChange={(e) => setSearchQuery(e.target.value)}
              className="pl-9"
            />
          </div>
          <div className="mt-2 flex justify-end">
            <ChannelFilter value={channelFilter} onChange={setChannelFilter} />
          </div>
        </div>

        <Tabs value={activeTab} onValueChange={(v) => setActiveTab(v as 'unassigned' | 'mine')}>
          <div className="px-4">
            <TabsList className="w-full">
              <TabsTrigger value="unassigned" className="flex-1">Unassigned</TabsTrigger>
              <TabsTrigger value="mine" className="flex-1">My Conversations</TabsTrigger>
            </TabsList>
          </div>

          <TabsContent value="unassigned" className="mt-0 flex-1 overflow-hidden">
            <ScrollArea className="h-[calc(100vh-200px)]">
              {unassignedQuery.isLoading && (
                <div className="flex items-center justify-center py-10">
                  <Loader2 className="h-5 w-5 animate-spin text-muted-foreground" />
                </div>
              )}
              {unassignedQuery.data?.conversations.length === 0 && !unassignedQuery.isLoading && (
                <div className="flex flex-col items-center justify-center py-10 text-muted-foreground">
                  <InboxIcon className="h-8 w-8 mb-2" />
                  <p className="text-sm">No unassigned conversations</p>
                </div>
              )}
              {conversations.map((conv) => (
                <ConversationItem
                  key={conv.id}
                  conversation={conv}
                  isActive={conv.id === activeConversationId}
                  onClick={() => handleSelectConversation(conv.id)}
                />
              ))}
            </ScrollArea>
          </TabsContent>

          <TabsContent value="mine" className="mt-0 flex-1 overflow-hidden">
            <ScrollArea className="h-[calc(100vh-200px)]">
              {myQuery.isLoading && (
                <div className="flex items-center justify-center py-10">
                  <Loader2 className="h-5 w-5 animate-spin text-muted-foreground" />
                </div>
              )}
              {myQuery.data?.conversations.length === 0 && !myQuery.isLoading && (
                <div className="flex flex-col items-center justify-center py-10 text-muted-foreground">
                  <MessageSquare className="h-8 w-8 mb-2" />
                  <p className="text-sm">No assigned conversations</p>
                </div>
              )}
              {conversations.map((conv) => (
                <ConversationItem
                  key={conv.id}
                  conversation={conv}
                  isActive={conv.id === activeConversationId}
                  onClick={() => handleSelectConversation(conv.id)}
                />
              ))}
            </ScrollArea>
          </TabsContent>
        </Tabs>
      </div>

      {/* ── Right Panel: Chat View ── */}
      <div className="flex flex-1 flex-col">
        {!activeConversationId ? (
          <div className="flex flex-1 flex-col items-center justify-center text-muted-foreground">
            <MessageSquare className="h-12 w-12 mb-3" />
            <p className="text-lg font-medium">Select a conversation</p>
            <p className="text-sm">Choose a conversation from the list to start chatting</p>
          </div>
        ) : (
          <>
            {/* ── Chat Header ── */}
            <div className="flex items-center justify-between border-b px-4 py-3">
              <div className="flex items-center gap-3">
                <div className="flex h-9 w-9 items-center justify-center rounded-full bg-primary/10 text-primary font-semibold text-sm">
                  {(activeConv?.contactName || activeConv?.contactPhone || '?').charAt(0).toUpperCase()}
                </div>
                <div>
                  <p className="text-sm font-medium">
                    {activeConv?.contactName || formatPhone(activeConv?.contactPhone ?? '')}
                    {activeConv && <ChannelBadge channel={activeConv.channel} />}
                  </p>
                  {activeConv?.contactName && (
                    <p className="text-xs text-muted-foreground">
                      {formatPhone(activeConv.contactPhone)}
                    </p>
                  )}
                  {activeConv?.channel === InboxChan.MBS && (
                    <p className="text-xs text-muted-foreground">
                      Via Meta Business Suite · Thread {activeConv.mbsThreadId.slice(-8)}
                      {activeConv.mbsPageId && ` · Page ${activeConv.mbsPageId}`}
                    </p>
                  )}
                </div>
                {convStatus && (
                  <StatusBadge
                    label={convStatus.label}
                    variant={convStatus.variant}
                    dot={convStatus.dot}
                    className="ml-2"
                  />
                )}
              </div>

              <DropdownMenu>
                <DropdownMenuTrigger asChild>
                  <Button variant="ghost" size="icon">
                    <MoreVertical className="h-4 w-4" />
                  </Button>
                </DropdownMenuTrigger>
                <DropdownMenuContent align="end">
                  <DropdownMenuItem
                    onClick={() => activeConversationId && claimMutation.mutate(activeConversationId)}
                    disabled={claimMutation.isPending}
                  >
                    <UserPlus className="mr-2 h-4 w-4" />
                    Claim
                  </DropdownMenuItem>
                  <DropdownMenuItem onClick={() => setShowTransfer(true)}>
                    <ArrowRightLeft className="mr-2 h-4 w-4" />
                    Transfer
                  </DropdownMenuItem>
                  <DropdownMenuSeparator />
                  <DropdownMenuItem
                    onClick={() => activeConversationId && closeMutation.mutate(activeConversationId)}
                    disabled={closeMutation.isPending}
                    className="text-destructive focus:text-destructive"
                  >
                    <XCircle className="mr-2 h-4 w-4" />
                    Close
                  </DropdownMenuItem>
                </DropdownMenuContent>
              </DropdownMenu>
            </div>

            {/* ── Messages Area ── */}
            <ScrollArea className="flex-1 px-4 py-4">
              {messagesQuery.isLoading && (
                <div className="flex items-center justify-center py-10">
                  <Loader2 className="h-5 w-5 animate-spin text-muted-foreground" />
                </div>
              )}
              <div className="flex flex-col gap-2">
                {messages.map((msg) => (
                  <MessageBubble key={msg.id} message={msg} />
                ))}
                <div ref={messagesEndRef} />
              </div>
              {isTyping && (
                <div className="mt-2 flex items-center gap-2 text-sm text-muted-foreground">
                  <span className="inline-flex gap-0.5">
                    <span className="h-1.5 w-1.5 animate-bounce rounded-full bg-muted-foreground" style={{ animationDelay: '0ms' }} />
                    <span className="h-1.5 w-1.5 animate-bounce rounded-full bg-muted-foreground" style={{ animationDelay: '150ms' }} />
                    <span className="h-1.5 w-1.5 animate-bounce rounded-full bg-muted-foreground" style={{ animationDelay: '300ms' }} />
                  </span>
                  typing...
                </div>
              )}
            </ScrollArea>

            {/* ── Send Bar ── */}
            <div className="border-t p-4">
              <div className="relative flex items-center gap-2">
                {showCanned && filteredCanned.length > 0 && (
                  <CannedDropdown
                    items={filteredCanned}
                    onSelect={handleCannedSelect}
                    onClose={() => setShowCanned(false)}
                  />
                )}
                <Textarea
                  placeholder="Type a message... (type / for canned responses)"
                  value={messageText}
                  onChange={handleInputChange}
                  onKeyDown={handleKeyDown}
                  rows={1}
                  className="flex-1 min-h-[38px] max-h-[120px] resize-none py-2"
                  style={{ height: 'auto', overflow: 'hidden' }}
                  onInput={(e) => {
                    const t = e.currentTarget
                    t.style.height = 'auto'
                    t.style.height = Math.min(t.scrollHeight, 120) + 'px'
                    t.style.overflow = t.scrollHeight > 120 ? 'auto' : 'hidden'
                  }}
                />
                <Button
                  size="icon"
                  onClick={handleSend}
                  disabled={!messageText.trim() || sendMutation.isPending}
                >
                  {sendMutation.isPending ? (
                    <Loader2 className="h-4 w-4 animate-spin" />
                  ) : (
                    <Send className="h-4 w-4" />
                  )}
                </Button>
              </div>
            </div>
          </>
        )}
      </div>

      {/* ── Transfer Dialog ── */}
      <TransferDialog
        open={showTransfer}
        onClose={() => setShowTransfer(false)}
        onConfirm={(targetId) =>
          activeConversationId &&
          transferMutation.mutate({ id: activeConversationId, targetUserId: targetId })
        }
        loading={transferMutation.isPending}
      />
    </div>
  )
}
