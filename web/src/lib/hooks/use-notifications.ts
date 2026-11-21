import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query'
import { api } from '@/lib/api-client'

// Something a customer was told about in their area of the panel.
export interface UserNotification {
  id: string
  kind: string
  title: string
  body: string
  resource_type: string
  resource_id: string
  read_at: string | null
  created_at: string
}

interface NotificationList {
  items: UserNotification[]
  unread: number
}

const KEY = ['notifications']

// useNotifications reads the signed-in customer's notifications. Staff get the
// audit log from the same endpoint, so this is for the customer area only.
export function useNotifications() {
  return useQuery<NotificationList>({
    queryKey: KEY,
    queryFn: async () => {
      const res = await api.get<UserNotification[]>('/api/v1/notifications')
      const body = res.data as unknown as { data: UserNotification[]; unread?: number }
      return { items: body.data ?? [], unread: body.unread ?? 0 }
    },
    refetchInterval: 60_000,
  })
}

export function useMarkNotificationRead() {
  const qc = useQueryClient()
  return useMutation<void, Error, string>({
    mutationFn: async (id) => {
      await api.put(`/api/v1/notifications/${id}/read`)
    },
    onSuccess: () => qc.invalidateQueries({ queryKey: KEY }),
  })
}

export function useMarkAllNotificationsRead() {
  const qc = useQueryClient()
  return useMutation<void, Error, void>({
    mutationFn: async () => {
      await api.put('/api/v1/notifications/read-all')
    },
    onSuccess: () => qc.invalidateQueries({ queryKey: KEY }),
  })
}

export interface NotificationPage {
  items: UserNotification[]
  unread: number
  total: number
}

// useNotificationPage reads one page of the customer's notifications, for the
// notifications page; the bell keeps the latest 30.
export function useNotificationPage(page: number, pageSize = 25) {
  return useQuery<NotificationPage>({
    queryKey: [...KEY, 'page', page, pageSize],
    queryFn: async () => {
      const res = await api.get<UserNotification[]>('/api/v1/notifications', { params: { limit: pageSize, offset: (page - 1) * pageSize } })
      const body = res.data as unknown as { data: UserNotification[]; unread?: number; total?: number }
      return { items: body.data ?? [], unread: body.unread ?? 0, total: body.total ?? 0 }
    },
  })
}
