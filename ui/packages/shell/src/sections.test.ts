import { describe, expect, it } from 'vitest'
import { itemActive, sectionFor, type Section } from './sections'

const sections: Section[] = [
  {
    label: 'Dashboard',
    tabs: [
      { to: '/dashboard', label: 'Overview' },
      { to: '/calendar', label: 'Schedule' },
    ],
  },
  {
    label: 'Integrations',
    tabs: [
      { to: '/plugins', label: 'Plugins' },
      { to: '/drivers', label: 'Drivers', anyOf: ['drivers.manage'] },
    ],
  },
]
const everyone = () => true
const nobody = () => false

describe('sectionFor', () => {
  it('finds the section of a page that is a tab', () => {
    expect(sectionFor(sections, '/calendar', everyone)?.tabs.map((t) => t.to)).toEqual([
      '/dashboard',
      '/calendar',
    ])
  })
  it('is null for a page that is not a tab, or under one', () => {
    expect(sectionFor(sections, '/pipelines', everyone)).toBeNull()
    expect(sectionFor(sections, '/dashboard/x', everyone)).toBeNull()
  })
  it('leaves out tabs a person may not open, and a strip of one', () => {
    expect(sectionFor(sections, '/plugins', nobody)).toBeNull()
    expect(sectionFor(sections, '/drivers', nobody)?.tabs.map((t) => t.to)).toEqual([
      '/plugins',
      '/drivers',
    ])
  })
})

describe('itemActive', () => {
  it('lights an item for its pages and what is under them', () => {
    expect(itemActive(['/lineage', '/dependencies'], '/dependencies')).toBe(true)
    expect(itemActive(['/pipelines'], '/pipelines/p1/changes/c1')).toBe(true)
    expect(itemActive(['/pipelines'], '/pipelinesx')).toBe(false)
  })
})
