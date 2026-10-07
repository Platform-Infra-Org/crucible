// Primary buttons take one hammer strike where the pointer came in. One delegated listener covers every
// button, present and future; the CSS (button.primary[data-spark]) draws the strike at --spark-x/--spark-y.
export function installSparks(doc: Document = document) {
  doc.addEventListener('pointerover', (e) => {
    const b = (e.target as Element | null)?.closest?.('button.primary:not(:disabled)')
    if (!(b instanceof HTMLElement) || b.contains(e.relatedTarget as Node | null)) return // moving within the button
    const r = b.getBoundingClientRect()
    b.style.setProperty('--spark-x', `${e.clientX - r.left}px`)
    b.style.setProperty('--spark-y', `${e.clientY - r.top}px`)
    delete b.dataset.spark
    void b.offsetWidth // reflow, so re-adding the attribute restarts the one-shot animation
    b.dataset.spark = ''
  })
  // Clear it once played, so a button re-enabled later (after a busy save) does not replay an old strike.
  doc.addEventListener('animationend', (e) => {
    if (e.animationName === 'strike-sparks' && e.target instanceof HTMLElement) delete e.target.dataset.spark
  })
}
