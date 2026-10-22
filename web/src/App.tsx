import { BrowserRouter, Link, Navigate, Route, Routes, useParams } from 'react-router'
import Layout from './components/Layout'
import ManageOnly from './components/ManageOnly'
import SectionGuard from './components/SectionGuard'
import ServerScope from './components/ServerScope'
import ClipBoard from './pages/ClipBoard'
import Clips from './pages/Clips'
import Home from './pages/Home'
import Login from './pages/Login'
import Manage from './pages/Manage'
import Pipeline from './pages/Pipeline'
import Queue from './pages/Queue'
import SeriesDetail from './pages/SeriesDetail'
import SeriesList from './pages/SeriesList'
import ServerSettings from './pages/ServerSettings'
import Suggestions from './pages/Suggestions'

function AdminServerRedirect() {
  const { serverId } = useParams()
  return <Navigate to={`/manage/servers/${serverId}`} replace />
}

export default function App() {
  return (
    <BrowserRouter>
      <Routes>
        <Route path="/login" element={<Login />} />
        <Route element={<Layout />}>
          <Route index element={<Home />} />
          <Route path="s/:serverId" element={<ServerScope />}>
            <Route index element={<SectionGuard section="videos" redirect><Queue /></SectionGuard>} />
            <Route path="pipeline" element={<SectionGuard section="videos"><Pipeline /></SectionGuard>} />
            <Route path="series" element={<SectionGuard section="videos"><SeriesList /></SectionGuard>} />
            <Route path="series/:id" element={<SectionGuard section="videos"><SeriesDetail /></SectionGuard>} />
            <Route path="suggestions" element={<SectionGuard section="suggestions"><Suggestions /></SectionGuard>} />
            <Route path="clips" element={<SectionGuard section="clips"><Clips /></SectionGuard>} />
            <Route path="clips/board" element={<SectionGuard section="clips"><ClipBoard /></SectionGuard>} />
          </Route>
          <Route element={<ManageOnly />}>
            <Route path="manage" element={<Manage />} />
            <Route path="manage/servers/:serverId" element={<ServerSettings />} />
          </Route>
          <Route path="admin" element={<Navigate to="/manage" replace />} />
          <Route path="admin/servers/:serverId" element={<AdminServerRedirect />} />
          <Route path="*" element={<p>Not found. <Link to="/">Go home</Link></p>} />
        </Route>
      </Routes>
    </BrowserRouter>
  )
}
