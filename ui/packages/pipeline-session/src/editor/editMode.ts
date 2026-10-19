/**
 * Whether the editor may change a pipeline, and how saving works.
 *
 * A pipeline managed in Git is saved through the application's gitSave
 * when it offers one (the edit is proposed, not written); without one it is
 * read only. A role that cannot edit pipelines makes every one read only.
 */
export function editMode(input: { managedInGit: boolean; canEdit: boolean; hasGitSave: boolean }): {
  readonlyReason: string
  viaGit: boolean
} {
  if (!input.canEdit)
    return { readonlyReason: 'Your role can view this pipeline but not change it.', viaGit: false }
  if (input.managedInGit && !input.hasGitSave) {
    return {
      readonlyReason:
        'This pipeline is managed in Git. Change it in its repository; edits here cannot be saved.',
      viaGit: false,
    }
  }
  return { readonlyReason: '', viaGit: input.managedInGit }
}
