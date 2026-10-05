const BASE_TITLE = 'Crucible'

export function toast(msg: string) {
  window.dispatchEvent(new CustomEvent('crucible-toast', { detail: msg }))
}

// alertUser shows an in-page toast and, when the tab is hidden, a browser notification + title badge (spec §8.6).
export function alertUser(msg: string, short = msg) {
  toast(msg)
  if (!document.hidden) return
  document.title = `⏳ ${short} · ${BASE_TITLE}`
  if ('Notification' in window && Notification.permission === 'granted') new Notification(BASE_TITLE, { body: msg })
  const restore = () => {
    document.title = BASE_TITLE
    document.removeEventListener('visibilitychange', restore)
  }
  document.addEventListener('visibilitychange', restore)
}

export function notificationsUndecided(): boolean {
  return 'Notification' in window && Notification.permission === 'default'
}

export async function askNotifications() {
  await Notification.requestPermission()
}
