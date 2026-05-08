// Background service worker.
// In M1 this only logs lifecycle events; M5 will route messages between content
// script and the daemon and surface daemon-side errors via chrome.notifications.

chrome.runtime.onInstalled.addListener((details) => {
  console.log('[DualSub] installed:', details.reason)
})

chrome.runtime.onStartup.addListener(() => {
  console.log('[DualSub] startup')
})
