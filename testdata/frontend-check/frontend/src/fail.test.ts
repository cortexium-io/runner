import { expect, it } from 'vitest'

it('retained fixture assertion preserves the actual failure', () => {
  expect({ visible: false }, `complete retained diagnostic ${'😀'.repeat(400)}`).toEqual({ visible: true })
})
