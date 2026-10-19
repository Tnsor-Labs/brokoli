import { describe, expect, it } from 'vitest'
import { editMode } from './editMode'

describe('editMode', () => {
  it('lets a pipeline made here be edited and saved directly', () => {
    expect(editMode({ managedInGit: false, canEdit: true, hasGitSave: false })).toEqual({
      readonlyReason: '',
      viaGit: false,
    })
    expect(editMode({ managedInGit: false, canEdit: true, hasGitSave: true })).toEqual({
      readonlyReason: '',
      viaGit: false,
    })
  })

  it('keeps a pipeline managed in Git read only unless the application can save it', () => {
    expect(
      editMode({ managedInGit: true, canEdit: true, hasGitSave: false }).readonlyReason,
    ).toMatch(/managed in Git/)
    expect(editMode({ managedInGit: true, canEdit: true, hasGitSave: true })).toEqual({
      readonlyReason: '',
      viaGit: true,
    })
  })

  it('never lets a role that cannot edit save, through Git or not', () => {
    for (const managedInGit of [true, false]) {
      const mode = editMode({ managedInGit, canEdit: false, hasGitSave: true })
      expect(mode.viaGit).toBe(false)
      expect(mode.readonlyReason).toMatch(/cannot|not change/)
    }
  })
})
