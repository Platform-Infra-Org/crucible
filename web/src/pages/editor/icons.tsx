// Small stroke icons for the editor (decorative: every control carrying one has its own accessible name).
const paths = {
  chevron: 'M6 4l4 4-4 4',
  folder: 'M1.5 4.5v8h13v-7H7.5L6 4H1.5z',
  'folder-open': 'M1.5 12.5v-8H6l1.5 1.5h6v2M1.5 12.5l2-5h11l-2 5z',
  file: 'M3.5 1.5h6l3 3v10h-9zM9.5 1.5v3h3',
  md: 'M3.5 1.5h6l3 3v10h-9zM9.5 1.5v3h3M5.5 12V8.5l1.5 1.5 1.5-1.5V12M10.5 8.5V12M9.5 11l1 1 1-1',
  yaml: 'M3.5 1.5h6l3 3v10h-9zM9.5 1.5v3h3M7 8c-1 0-1 .5-1 1.25S5.5 10.5 5 10.5c.5 0 1 .5 1 1.25S6 13 7 13M9 8c1 0 1 .5 1 1.25s.5 1.25 1 1.25c-.5 0-1 .5-1 1.25S10 13 9 13',
  sh: 'M3.5 1.5h6l3 3v10h-9zM9.5 1.5v3h3M5.5 9l2 1.5-2 1.5M8.5 12.5h2',
  files: 'M5.5 1.5h6l3 3v8h-9zM11.5 1.5v3h3M3.5 4.5h-2v10h8v-2',
  problems: 'M8 1.5l6.5 12h-13zM8 6v3.5M8 11.5v.5',
  changes: 'M4.5 2v7M4.5 9a2 2 0 1 0 0 4 2 2 0 0 0 0-4zM11.5 3a2 2 0 1 0 0 4 2 2 0 0 0 0-4zM11.5 7c0 3-7 2-7 4',
  blocks: 'M1.5 9.5h6v5h-6zM8.5 9.5h6v5h-6zM5 3.5h6v5H5z',
  'new-file': 'M3.5 1.5h6l3 3v4M9.5 1.5v3h3M3.5 1.5v13h5M12 10v5M9.5 12.5h5',
  'new-module': 'M1.5 4.5v8h7M1.5 4.5H6l1.5 1.5h7v3M12 9.5v5M9.5 12h5',
  'collapse-all': 'M3.5 3.5h9v9h-9zM6 8h4',
  close: 'M4 4l8 8M12 4l-8 8',
  preview: 'M1.5 2.5h13v11h-13zM9 2.5v11',
} as const
export type IconName = keyof typeof paths

export function Icon({ name, className }: { name: IconName; className?: string }) {
  return (
    <svg className={`icon${className ? ` ${className}` : ''}`} viewBox="0 0 16 16" width="16" height="16" aria-hidden="true" focusable="false">
      <path d={paths[name]} />
    </svg>
  )
}
