export async function copyText(text) {
  if (window.isSecureContext && navigator.clipboard) {
    try {
      await navigator.clipboard.writeText(text)
      return
    } catch (error) {
      if (!document.queryCommandSupported || !document.queryCommandSupported('copy')) throw error
    }
  }
  const area = document.createElement('textarea')
  area.value = text
  area.setAttribute('readonly', '')
  area.style.cssText = 'position: fixed; top: 0; left: 0; width: 1px; height: 1px; opacity: 0;'
  const focused = document.activeElement
  document.body.appendChild(area)
  area.select()
  const copied = document.execCommand('copy')
  document.body.removeChild(area)
  if (focused && focused.focus) focused.focus()
  if (!copied) throw new Error('copy command failed')
}
