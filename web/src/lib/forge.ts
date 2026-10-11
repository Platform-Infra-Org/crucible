// article is "a" or "an" for a rank name ("an Apprentice", "a Blade").
export const article = (t: string) => (/^[AEIOU]/i.test(t) ? 'an' : 'a')
