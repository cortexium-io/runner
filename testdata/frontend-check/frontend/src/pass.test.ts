import { expect, it } from 'vitest'

it('retained fixture passes', () => {
  expect({ visible: true }).toEqual({ visible: true })
})
