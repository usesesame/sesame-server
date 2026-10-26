import { findSecretShapedText, SUPPORT_CATEGORIES, type SupportCategory, type SupportTicketSummary } from './support'

export interface HelpEntry {
  id: string
  categories: SupportCategory[]
  keywords: string[]
  title: string
  answer: string
  link?: { label: string; path: string; site?: boolean }
}

const MIN_TEXT_LENGTH = 8
const MAX_ANSWERS = 3

function normalise(text: string): string {
  return ` ${text.toLowerCase().replaceAll('’', "'").replace(/[^\p{L}\p{N}']+/gu, ' ').trim()} `
}

export function matchHelp(entries: HelpEntry[], category: SupportCategory, text: string, limit = MAX_ANSWERS): HelpEntry[] {
  const typed = normalise(text)
  if (typed.trim().length < MIN_TEXT_LENGTH) return []
  const scored: Array<{ entry: HelpEntry; score: number; order: number }> = []
  entries.forEach((entry, order) => {
    const hits = new Set(entry.keywords.map(normalise).filter((keyword) => typed.includes(keyword))).size
    if (hits === 0) return
    scored.push({ entry, score: hits * 2 + (entry.categories.includes(category) ? 1 : 0), order })
  })
  return scored
    .sort((left, right) => right.score - left.score || left.order - right.order)
    .slice(0, limit)
    .map((item) => item.entry)
}

export function relatedOpenTickets(tickets: SupportTicketSummary[], category: SupportCategory): SupportTicketSummary[] {
  if (category === 'general') return []
  return tickets
    .filter((ticket) => ticket.category === category && ticket.status !== 'closed')
    .sort((left, right) => new Date(right.updatedAt).getTime() - new Date(left.updatedAt).getTime())
}

export interface SupportDraft {
  category: SupportCategory
  subject: string
  message: string
}

const DRAFT_KEY = 'sesame-support-draft-v1'
const MAX_SUBJECT = 120
const MAX_MESSAGE = 4000

export function saveSupportDraft(draft: SupportDraft): void {
  try {
    if (draft.subject.trim() === '' && draft.message.trim() === '') {
      sessionStorage.removeItem(DRAFT_KEY)
      return
    }
    if (findSecretShapedText(`${draft.subject}\n${draft.message}`)) {
      sessionStorage.removeItem(DRAFT_KEY)
      return
    }
    sessionStorage.setItem(DRAFT_KEY, JSON.stringify(draft))
  } catch {
    return
  }
}

export function loadSupportDraft(): SupportDraft | null {
  try {
    const raw = sessionStorage.getItem(DRAFT_KEY)
    if (!raw) return null
    const parsed = JSON.parse(raw) as Partial<SupportDraft> | null
    if (!parsed || typeof parsed.subject !== 'string' || typeof parsed.message !== 'string') return null
    const category = SUPPORT_CATEGORIES.some((option) => option.value === parsed.category) ? (parsed.category as SupportCategory) : 'general'
    return { category, subject: parsed.subject.slice(0, MAX_SUBJECT), message: parsed.message.slice(0, MAX_MESSAGE) }
  } catch {
    return null
  }
}

export function clearSupportDraft(): void {
  try {
    sessionStorage.removeItem(DRAFT_KEY)
  } catch {
    return
  }
}
