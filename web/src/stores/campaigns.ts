import { create } from 'zustand'
import type { WsCampaignProgressPayload, WsCampaignStatusPayload, CampaignStatus } from '@/api/types'

interface CampaignsState {
  progress: Record<string, WsCampaignProgressPayload>
  statusChanges: Record<string, { status: CampaignStatus; reason: string }>
  updateProgress: (data: WsCampaignProgressPayload) => void
  updateStatus: (data: WsCampaignStatusPayload) => void
  clearProgress: (campaignId: string) => void
}

export const useCampaignsStore = create<CampaignsState>((set) => ({
  progress: {},
  statusChanges: {},

  updateProgress: (data) =>
    set((s) => ({ progress: { ...s.progress, [data.campaignId]: data } })),

  updateStatus: (data) =>
    set((s) => ({
      statusChanges: {
        ...s.statusChanges,
        [data.campaignId]: { status: data.newStatus, reason: data.reason },
      },
    })),

  clearProgress: (campaignId) =>
    set((s) => {
      const { [campaignId]: _, ...rest } = s.progress
      return { progress: rest }
    }),
}))
