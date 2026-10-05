const builtIn = [
  'Steel is forged in fire.',
  "The crucible doesn't break the metal. It reveals it.",
  'Every master was once a lump of ore.',
  'Heat, hammer, repeat.',
  'Pressure makes diamonds; heat makes blades.',
  'Strike while the iron is hot.',
  'A blade is only as good as its tempering.',
  'Sparks fly when skill meets effort.',
]
let quotes = [...builtIn]

export function addQuotes(extra: string[]) {
  quotes = Array.from(new Set([...quotes, ...extra]))
}

export function randomQuote(): string {
  return quotes[Math.floor(Math.random() * quotes.length)]
}
