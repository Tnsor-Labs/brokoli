import { useCallback, useEffect, useRef, useState } from 'react'
import type { Pipeline, PipelineEdge, PipelineNode } from '@brokoli/api'

export type Doc = { pipeline: Pipeline; nodes: PipelineNode[]; edges: PipelineEdge[] }
type Snapshot = { nodes: PipelineNode[]; edges: PipelineEdge[] }

/*
 * Editor document with undo/redo and save tracking.
 *
 * - Every change produces new arrays and objects, so a snapshot is just the
 *   previous references; nothing is deep-copied.
 * - Changes that pass a string history key coalesce: a burst of keystrokes
 *   in one inspector field is one undo step, not one per character.
 * - Undo covers the graph (nodes and edges). Pipeline settings (schedule,
 *   tags, dependencies) are saved like everything else but are not undoable,
 *   the same scope as the previous editor.
 * - `dirty` compares the revision now with the revision at the last
 *   successful save, so edits made while a save is in flight stay dirty.
 */
export function useDocument(initial: Pipeline | undefined) {
  const [doc, setDoc] = useState<Doc | null>(null)
  const docRef = useRef<Doc | null>(null)
  const revision = useRef(0)
  const [rev, setRev] = useState(0)
  const [savedRev, setSavedRev] = useState(0)
  const undoStack = useRef<Snapshot[]>([])
  const redoStack = useRef<Snapshot[]>([])
  const lastKey = useRef<{ key: string; at: number } | null>(null)

  const commit = (next: Doc) => {
    docRef.current = next
    setDoc(next)
    revision.current++
    setRev(revision.current)
  }

  const reset = useCallback((p: Pipeline) => {
    const next = { pipeline: p, nodes: p.nodes ?? [], edges: p.edges ?? [] }
    docRef.current = next
    setDoc(next)
    undoStack.current = []
    redoStack.current = []
    lastKey.current = null
    revision.current++
    setRev(revision.current)
    setSavedRev(revision.current)
  }, [])

  useEffect(() => {
    if (initial && !docRef.current) reset(initial)
  }, [initial, reset])

  const change = useCallback((fn: (d: Doc) => Doc, history: boolean | string = true) => {
    const current = docRef.current
    if (!current) return
    if (history) {
      const now = Date.now()
      const coalesce = typeof history === 'string' && lastKey.current?.key === history && now - lastKey.current.at < 1200
      if (!coalesce) {
        undoStack.current = [...undoStack.current.slice(-99), { nodes: current.nodes, edges: current.edges }]
        redoStack.current = []
      }
      lastKey.current = typeof history === 'string' ? { key: history, at: now } : null
    }
    commit(fn(current))
  }, [])

  const step = useCallback((from: typeof undoStack, to: typeof undoStack) => {
    const current = docRef.current
    const snap = from.current[from.current.length - 1]
    if (!current || !snap) return false
    from.current = from.current.slice(0, -1)
    to.current = [...to.current, { nodes: current.nodes, edges: current.edges }]
    lastKey.current = null
    commit({ ...current, nodes: snap.nodes, edges: snap.edges })
    return true
  }, [])

  const undo = useCallback(() => step(undoStack, redoStack), [step])
  const redo = useCallback(() => step(redoStack, undoStack), [step])

  /** After a save: adopt the server's copy if nothing changed meanwhile, then record the save point. */
  const saved = useCallback((serverCopy: Pipeline, revisionAtSave: number) => {
    const current = docRef.current
    if (current && revision.current === revisionAtSave) {
      const next = { pipeline: serverCopy, nodes: serverCopy.nodes ?? [], edges: serverCopy.edges ?? [] }
      docRef.current = next
      setDoc(next)
    } else if (current) {
      const next = { ...current, pipeline: { ...current.pipeline, updated_at: serverCopy.updated_at, draft: serverCopy.draft } }
      docRef.current = next
      setDoc(next)
    }
    setSavedRev(revisionAtSave)
  }, [])

  return {
    doc,
    docRef,
    change,
    undo,
    redo,
    reset,
    saved,
    revision,
    dirty: rev !== savedRev,
    canUndo: undoStack.current.length > 0,
    canRedo: redoStack.current.length > 0,
  }
}
