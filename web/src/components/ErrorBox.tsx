import { Link } from 'react-router'
import { ApiError } from '../api'

export function ErrorBox({ error }: { error: ApiError }) {
  const msg = error.status === 423 ? 'Finish the earlier modules first.' : error.message
  return (
    <div className="center">
      <div>
        <h2>The forge sputtered</h2>
        <p className="error">{msg}</p>
        <Link to="/">Back to the Hearth</Link>
      </div>
    </div>
  )
}
